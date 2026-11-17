// Package upstream forwards requests to the DSM WebAPI (Standard
// http://127.0.0.1:5000) – für SYNO.API.Auth und alle Nicht-Video-Station-APIs.
package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Client talks to DSM.
type Client struct {
	base atomic.Pointer[url.URL]
	HTTP *http.Client
}

// BaseURL returns the current DSM address.
func (c *Client) BaseURL() *url.URL { return c.base.Load() }

// SetBase changes the DSM address at runtime (Portumstellung 5000 → 5010).
func (c *Client) SetBase(base string) error {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return err
	}
	c.base.Store(u)
	return nil
}

// New creates an upstream client. TLS verification is skipped only for
// loopback targets (DSM-Zertifikat passt nie auf 127.0.0.1).
func New(base string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, err
	}
	insecure := false
	if h := u.Hostname(); h == "127.0.0.1" || h == "localhost" || h == "::1" {
		insecure = true
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec // nur Loopback
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true, // Bytes unverändert durchreichen
	}
	c := &Client{HTTP: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
	c.base.Store(u)
	return c, nil
}

var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// Forward sends the original request (with already-read body) upstream and
// streams the response back. If inspect != nil and the response is JSON, the
// decoded body is passed to inspect before the bytes are written unchanged.
func (c *Client) Forward(w http.ResponseWriter, r *http.Request, body []byte, inspect func(map[string]any)) {
	var f func(map[string]any) []byte
	if inspect != nil {
		f = func(m map[string]any) []byte { inspect(m); return nil }
	}
	c.ForwardFilter(w, r, body, f)
}

// ForwardFilter wie Forward; liefert filter eine Antwort zurück (≠ nil), wird
// diese statt der DSM-Antwort gesendet (z.B. Anmeldung abgelehnt).
func (c *Client) ForwardFilter(w http.ResponseWriter, r *http.Request, body []byte, inspect func(map[string]any) []byte) {
	b := c.BaseURL()
	target := *b
	target.Path = strings.TrimRight(b.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "bad upstream request", http.StatusBadGateway)
		return
	}
	for k, vs := range r.Header {
		if hopHeaders[k] || k == "Content-Length" {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// Ursprünglichen Host beibehalten (DSM erzeugt damit korrekte Links/Weiterleitungen).
	req.Host = r.Host
	req.Header.Set(LoopHeader, "1")
	if ip := clientIP(r); ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		http.Error(w, "DSM nicht erreichbar", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		if hopHeaders[k] || k == "Content-Length" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	isJSON := strings.Contains(resp.Header.Get("Content-Type"), "json")
	if inspect != nil && isJSON {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err == nil {
			if m := DecodeJSON(resp.Header, raw); m != nil {
				upCookies := len(w.Header().Values("Set-Cookie"))
				if repl := inspect(m); repl != nil {
					// Cookies der ersetzten DSM-Antwort verwerfen, vom Aufrufer
					// gesetzte (z.B. id der Ersatzanmeldung) behalten.
					keep := append([]string(nil), w.Header().Values("Set-Cookie")[upCookies:]...)
					w.Header().Del("Set-Cookie")
					for _, ck := range keep {
						w.Header().Add("Set-Cookie", ck)
					}
					w.Header().Del("Content-Encoding")
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(repl)
					return
				}
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(flushWriter{w}, resp.Body)
}

// Proxy returns a transparent reverse proxy to DSM (inkl. WebSocket-Upgrade,
// z.B. für Benachrichtigungen der DSM-Oberfläche im Port-Modus 5000/5001).
// LoopHeader kennzeichnet Anfragen dieses Pakets an DSM. DSMs Webserver reicht
// Aufrufe der DS-video-Apps an das Paket weiter – aber nie solche mit diesem
// Kopf (sonst Schleife, da durchgereichte Anfragen den App-User-Agent behalten).
const LoopHeader = "X-DSVR-Loop"

func (c *Client) Proxy() http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(c.BaseURL())
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("X-Forwarded-For")
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-For", ClientIP(pr.In)) // echte Client-Adresse für DSMs Sperrliste
			pr.Out.Header.Set(LoopHeader, "1")
		},
		// Eigener Transport ohne Antwort-Zeitlimit: DSM antwortet z.B. bei
		// Paketinstallationen oder Datei-Uploads erst nach Minuten.
		Transport:     c.proxyTransport(),
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "DSM nicht erreichbar: "+err.Error(), http.StatusBadGateway)
		},
	}
}

func (c *Client) proxyTransport() http.RoundTripper {
	tr, ok := c.HTTP.Transport.(*http.Transport)
	if !ok {
		return c.HTTP.Transport
	}
	t := tr.Clone()
	t.ResponseHeaderTimeout = 0
	return t
}

// Call performs a server-side WebAPI call (form POST to entry.cgi).
func (c *Client) Call(ctx context.Context, cgi string, params url.Values) (map[string]any, error) {
	b := c.BaseURL()
	target := *b
	target.Path = strings.TrimRight(b.Path, "/") + "/webapi/" + cgi
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set(LoopHeader, "1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	m := DecodeJSON(resp.Header, raw)
	if m == nil {
		return nil, io.ErrUnexpectedEOF
	}
	return m, nil
}

// CallRaw performs a WebAPI call and returns the raw body and content type
// (z.B. Zertifikat-Export als ZIP).
func (c *Client) CallRaw(ctx context.Context, cgi string, params url.Values) ([]byte, string, error) {
	b := c.BaseURL()
	target := *b
	target.Path = strings.TrimRight(b.Path, "/") + "/webapi/" + cgi
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(params.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set(LoopHeader, "1")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return raw, resp.Header.Get("Content-Type"), err
}

// GetRaw performs a GET on /webapi/<cgi> with extra headers (z.B. X-SYNO-TOKEN
// für Download-Aufrufe wie den Zertifikatsexport).
func (c *Client) GetRaw(ctx context.Context, cgi string, params url.Values, hdr map[string]string) ([]byte, string, error) {
	b := c.BaseURL()
	target := *b
	target.Path = strings.TrimRight(b.Path, "/") + "/webapi/" + cgi
	target.RawQuery = params.Encode()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set(LoopHeader, "1")
	for k, v := range hdr {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return raw, resp.Header.Get("Content-Type"), err
}

// DecodeJSON decodes a (possibly gzip-encoded) JSON body.
func DecodeJSON(h http.Header, raw []byte) map[string]any {
	data := raw
	if strings.EqualFold(h.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil
		}
		data, err = io.ReadAll(io.LimitReader(zr, 64<<20))
		if err != nil {
			return nil
		}
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

func clientIP(r *http.Request) string { return ClientIP(r) }

// ClientIP liefert die echte Client-Adresse. Kommt die Anfrage über einen
// Proxy im eigenen Netz (DSMs Webserver auf 127.0.0.1, Nginx Proxy Manager im
// LAN), steht sie in X-Forwarded-For. Gelesen wird von rechts (diese Einträge
// haben die eigenen Proxys gesetzt) bis zur ersten öffentlichen Adresse –
// vorangestellte, gefälschte Einträge eines Angreifers werden so ignoriert.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !(ip.IsLoopback() || ip.IsPrivate()) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	var hops []string
	for _, h := range xff {
		for _, p := range strings.Split(h, ",") {
			if p = strings.TrimSpace(p); p != "" {
				hops = append(hops, p)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hip := net.ParseIP(hops[i])
		if hip == nil {
			break
		}
		host = hops[i]
		if !(hip.IsLoopback() || hip.IsPrivate()) {
			break
		}
	}
	return host
}

type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

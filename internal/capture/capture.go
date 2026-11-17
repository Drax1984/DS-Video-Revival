// Package capture: transparenter Mitschnitt-Proxy zwischen einem ORIGINAL-
// DS-Video-Client und einer ORIGINAL-Video-Station (eigene, autorisierte
// Testinstallation). Protokolliert API-Name, Version, Methode, Parameter und
// Antwortstruktur als JSONL – Zugangsdaten werden vor dem Schreiben entfernt.
//
// Zweck: Ersetzt Annahmen durch Belege (Regel 6). Ausgabe mit `dsvr analyze`
// auswerten und an die Entwicklung zurückgeben.
package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/webapi"
)

// Entry is one captured exchange.
type Entry struct {
	Time       time.Time         `json:"time"`
	Client     string            `json:"client"`
	UserAgent  string            `json:"user_agent"`
	HTTPMethod string            `json:"http_method"`
	Path       string            `json:"path"`
	API        string            `json:"api,omitempty"`
	Version    int               `json:"version,omitempty"`
	Method     string            `json:"method,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	ReqHeaders map[string]string `json:"req_headers,omitempty"`
	Status     int               `json:"status"`
	RespType   string            `json:"resp_type"`
	RespLen    int64             `json:"resp_len"`
	RespRange  string            `json:"resp_range,omitempty"`
	RespJSON   any               `json:"resp_json,omitempty"`
	DurationMS int64             `json:"duration_ms"`
}

// Secret parameters – Werte werden nie geschrieben.
var secretParams = map[string]bool{"passwd": true, "password": true, "otp_code": true, "__cIpHeRtExT": true, "device_id": true, "device_name": false, "SynoToken": true, "_sid": true, "sid": true, "token": true, "client_secret": true}

var sidLike = regexp.MustCompile(`^[A-Za-z0-9_\-\.]{30,}$`)

// Redact returns a privacy-safe parameter map.
func Redact(v url.Values) map[string]string {
	out := map[string]string{}
	for k, vals := range v {
		val := strings.Join(vals, ",")
		switch {
		case secretParams[k] && (k == "_sid" || k == "sid"):
			out[k] = "<sid:" + short(val) + ">"
		case secretParams[k]:
			out[k] = "<entfernt>"
		case k == "account":
			out[k] = "<konto>"
		default:
			if len(val) > 2000 {
				val = val[:2000] + "…"
			}
			out[k] = val
		}
	}
	return out
}

func short(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}

// RedactJSON removes sids/tokens from response data recursively.
func RedactJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			lk := strings.ToLower(k)
			if lk == "sid" || lk == "synotoken" || lk == "did" || lk == "token" || lk == "device_id" {
				if s, ok := x.(string); ok {
					t[k] = "<" + lk + ":" + short(s) + ">"
					continue
				}
			}
			t[k] = RedactJSON(x)
		}
		return t
	case []any:
		for i := range t {
			t[i] = RedactJSON(t[i])
		}
		return t
	}
	return v
}

// Proxy is the capturing reverse proxy.
type Proxy struct {
	Up  *upstream.Client
	out io.Writer
	mu  sync.Mutex
}

// NewProxy writes entries to path (append).
func NewProxy(upstreamURL, path string) (*Proxy, error) {
	up, err := upstream.New(upstreamURL)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Proxy{Up: up, out: f}, nil
}

type recorder struct {
	http.ResponseWriter
	status int
	n      int64
	buf    *bytes.Buffer
	keep   bool
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	ct := r.Header().Get("Content-Type")
	r.keep = strings.Contains(ct, "json") || strings.Contains(ct, "mpegurl") || strings.HasPrefix(ct, "text/")
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(200)
	}
	if r.keep && r.buf.Len() < 4<<20 {
		r.buf.Write(p)
	}
	n, err := r.ResponseWriter.Write(p)
	r.n += int64(n)
	return n, err
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 32<<20))
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	req, _ := webapi.Parse(r)
	r.Body = io.NopCloser(bytes.NewReader(body))

	rec := &recorder{ResponseWriter: w, buf: &bytes.Buffer{}}
	p.Up.Forward(rec, r, body, nil)

	e := Entry{Time: start, Client: r.RemoteAddr, UserAgent: r.UserAgent(), HTTPMethod: r.Method, Path: r.URL.Path,
		Status: rec.status, RespType: rec.Header().Get("Content-Type"), RespLen: rec.n,
		RespRange: rec.Header().Get("Content-Range"), DurationMS: time.Since(start).Milliseconds()}
	if req != nil {
		e.API, e.Version, e.Method = req.API, req.Version, req.Method
		e.Params = Redact(req.Params)
	}
	e.ReqHeaders = map[string]string{}
	for _, h := range []string{"Range", "Accept", "Content-Type", "X-Syno-Token", "Accept-Encoding"} {
		if v := r.Header.Get(h); v != "" {
			if h == "X-Syno-Token" {
				v = "<token:" + short(v) + ">"
			}
			e.ReqHeaders[h] = v
		}
	}
	if rec.keep && rec.buf.Len() > 0 {
		if m := upstream.DecodeJSON(rec.Header(), rec.buf.Bytes()); m != nil {
			e.RespJSON = RedactJSON(any(m))
		} else if strings.Contains(e.RespType, "mpegurl") {
			e.RespJSON = map[string]any{"_m3u8": truncate(rec.buf.String(), 4000)}
		}
	}
	p.mu.Lock()
	b, _ := json.Marshal(e)
	_, _ = p.out.Write(append(b, '\n'))
	p.mu.Unlock()
	log.Printf("capture: %s %s api=%s v%d %s → %d (%d B)", r.Method, r.URL.Path, e.API, e.Version, e.Method, e.Status, e.RespLen)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

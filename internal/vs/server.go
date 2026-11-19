// Package vs is the DS Video Compatibility API (Komponente B).
package vs

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // DSM bringt nicht immer vollständige Zeitzonendaten mit

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/listen"
	"dsvideorevival/internal/metadata"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/webapi"
)

// Server bundles all components.
type Server struct {
	cfg       atomic.Pointer[config.Config]
	Auth      *auth.Adapter
	Up        *upstream.Client // nil im lokalen Modus
	Lib       *library.Index
	Streams   *stream.Manager
	Rec       *Recorder
	Registry  map[string]webapi.APIDef
	FFprobe   string // leer = keine technischen Dateidaten
	HLS       *stream.HLSManager
	Offline   *stream.OfflineManager // Offline-Umwandlung (nil = nicht verfügbar)
	Listen    *listen.Manager
	proxy     http.Handler
	proxyOnce sync.Once
	Meta      *metadata.Engine
	Started   time.Time
	// AdminHandler is mounted under /dsvr/.
	AdminHandler http.Handler
	// LogCalls protokolliert jeden Video-Station-Aufruf (api/method/version).
	LogCalls bool

	connsOnce sync.Once
	conns     *Conns

	// Jellyfin: Jellyfin-kompatible Schnittstelle (nil = nicht eingebunden).
	Jellyfin JellyfinAPI
}

// Cfg returns the current configuration (atomar austauschbar über die Admin-UI).
func (s *Server) Cfg() *config.Config { return s.cfg.Load() }

// SetCfg replaces the configuration.
func (s *Server) SetCfg(c config.Config) { s.cfg.Store(&c) }

// Ctx is passed to handlers.
type Ctx struct {
	W    http.ResponseWriter
	Req  *webapi.Request
	Sess auth.Session
	S    *Server
	Lib  *library.View // Sicht des Benutzers (Freigaben, Jugendschutz)
	// Blocked: zweite Verbindung eines Kontos mit „Nur eine Verbindung“ –
	// Lib ist leer, Wiedergabe gesperrt.
	Blocked bool
}

type handlerFunc func(c *Ctx)

// Status-Kategorien für docs/compatibility.md und die Admin-Oberfläche.
const (
	StBelegt     = "belegt"     // Antwortstruktur aus realer Antwort/Schema bzw. verifiziertem Proxy
	StTeilbelegt = "teilbelegt" // Hülle belegt, Inhalt teilweise Hypothese
	StHypothese  = "hypothese"  // nicht belegt – nur mit experimental_shapes oder dokumentierter Ausnahme
)

type impl struct {
	fn     handlerFunc
	status string
	note   string
}

// ServeHTTP routes all requests.
// DSMPrefix: Pfad, unter dem DSMs eigener Webserver (Port 5000/5001, wie
// früher Video Station) Aufrufe an dieses Paket weiterreicht – eingerichtet
// über die nginx-Konfiguration des Pakets (web-config, Typ „dsm“).
const DSMPrefix = "/webapi/_dsvr"

type ctxKey int

const ctxViaDSM ctxKey = 1

// ViaDSM: Anfrage kam über DSMs Webserver (Weiterleitung unter DSMPrefix).
func ViaDSM(r *http.Request) bool { v, _ := r.Context().Value(ctxViaDSM).(bool); return v }

// HLSPrefix: absoluter Pfad der HLS-Dateien eines Jobs, passend zum Zugangsweg.
func HLSPrefix(r *http.Request, id string) string {
	if ViaDSM(r) {
		return DSMPrefix + "/hls/" + id + "/"
	}
	return "/hls/" + id + "/"
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.HasPrefix(p, DSMPrefix+"/") {
		// /webapi/_dsvr/entry.cgi → /webapi/entry.cgi, /webapi/_dsvr/hls/… → /hls/…
		rest := strings.TrimPrefix(p, DSMPrefix)
		if !strings.HasPrefix(rest, "/hls/") {
			rest = "/webapi" + rest
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxViaDSM, true))
		r.URL.Path, r.URL.RawPath = rest, ""
		p = rest
		if s.LogCalls && !strings.HasSuffix(p, ".m4s") && !strings.HasSuffix(p, ".ts") {
			// Zugriffsprotokoll für den Weg über DSM (ohne Videosegmente):
			// zeigt auch Aufrufe des iOS-Players oder abgelehnte Anfragen.
			sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
			w = sw
			start := time.Now()
			defer func() {
				log.Printf("dsm-zugriff: %s %s → %d (%s, %s, %dms)", r.Method, p, sw.code, clientAddr(r), shortUA(r.UserAgent()), time.Since(start).Milliseconds())
			}()
		}
	}
	if s.serveJellyfin(w, r) {
		return
	}
	switch {
	case strings.HasPrefix(p, "/dsvr/") || p == "/dsvr":
		if s.AdminHandler != nil {
			s.AdminHandler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	case strings.HasPrefix(p, "/webapi/"):
		s.serveWebAPI(w, r)
		return
	case strings.HasPrefix(p, "/hls/") && s.HLS != nil:
		// /hls/<job>/<datei> – Job-ID ist das (nicht erratbare) Zugriffstoken.
		rest := strings.TrimPrefix(p, "/hls/")
		id, name, _ := strings.Cut(rest, "/")
		if j, ok := s.HLS.Get(id); ok {
			if key, blocked := s.Conns().Stream(id); blocked {
				http.Error(w, "Konto ist auf einem anderen Gerät aktiv", http.StatusForbidden)
				return
			} else if key != "" {
				gone := s.Conns().Begin(key, true)
				defer s.Conns().End(key, true)
				w = GuardWriter(w, gone)
			}
			s.HLS.ServeFile(w, r, j, name, HLSPrefix(r, id))
			return
		}
		http.NotFound(w, r)
		return
	}
	// Port-Modus: Browser, die die DSM-Oberfläche öffnen, direkt zu DSM
	// (5010/5011) schicken. So hängt die DSM-Verwaltung nie an diesem Paket –
	// sonst bricht z.B. ein Paket-Update ab, sobald DSM dieses Paket stoppt.
	if target, ok := s.dsmUIRedirect(r); ok {
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	// Alles andere (z.B. /webman/…, WebSockets) im DSM-Modus transparent durchreichen.
	if s.Up != nil {
		s.proxyOnce.Do(func() { s.proxy = s.Up.Proxy() })
		s.proxy.ServeHTTP(w, r)
		return
	}
	if p == "/" {
		http.Redirect(w, r, "/dsvr/", http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) serveWebAPI(w http.ResponseWriter, r *http.Request) {
	// Body puffern: wird ggf. unverändert an DSM weitergereicht. Große Rümpfe
	// (Uploads, z.B. Paketinstallation, File Station) sind nie Video-Station-
	// Aufrufe: sie werden ungekürzt und ungepuffert an DSM durchgestreamt.
	var raw []byte
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut) {
		if r.ContentLength > maxBufferedBody && s.Up != nil {
			s.passthrough(w, r)
			return
		}
		var err error
		raw, err = io.ReadAll(io.LimitReader(r.Body, maxBufferedBody+1))
		if err != nil {
			http.Error(w, "Lesefehler", http.StatusBadRequest)
			return
		}
		if len(raw) > maxBufferedBody { // Länge unbekannt (chunked) und groß
			if s.Up == nil {
				http.Error(w, "Anfrage zu groß", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
			s.passthrough(w, r)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	req, err := webapi.Parse(r)
	if err != nil {
		webapi.Fail(w, webapi.ErrNoParameter)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if ViaDSM(r) && s.LogCalls && !isVideoStationAPI(req.API) {
		log.Printf("dsm: %s %s über DSM von %s (%s)", req.API, req.Method, clientAddr(r), r.UserAgent())
	}

	switch {
	case req.API == "SYNO.API.Info":
		s.handleAPIInfo(w, req)
		return
	case req.API == "SYNO.API.Encryption" && s.Auth.HandleEncryption(w, req):
		return
	case req.API == "SYNO.API.Auth":
		r.Body = io.NopCloser(bytes.NewReader(raw))
		s.Auth.HandleAuth(w, req, raw)
		return
	case isVideoStationAPI(req.API):
		s.handleVS(w, req)
		return
	}
	if s.Up != nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		if ViaDSM(r) && strings.HasSuffix(r.URL.Path, "/query.cgi") {
			// DSM leitet query.cgi der DS-video-Apps hierher; Fremd-APIs über
			// entry.cgi zurückgeben, sonst schickte DSM sie erneut hierher.
			r.URL.Path = strings.TrimSuffix(r.URL.Path, "query.cgi") + "entry.cgi"
		}
		if ViaDSM(r) && req.API != "" && s.LogCalls {
			// App-Aufrufe von DSM-APIs: Fehlercodes protokollieren (Diagnose).
			api, method := req.API, req.Method
			s.Up.Forward(w, r, raw, func(m map[string]any) {
				if ok, _ := m["success"].(bool); !ok {
					log.Printf("dsm: %s %s → Fehler %v", api, method, m["error"])
				}
			})
			return
		}
		s.proxyOnce.Do(func() { s.proxy = s.Up.Proxy() })
		s.proxy.ServeHTTP(w, r)
		return
	}
	if req.API == "" {
		webapi.Fail(w, webapi.ErrNoParameter)
		return
	}
	s.Rec.Record(req.API, req.Version, req.Method, "fremde API (lokaler Modus)", paramKeys(req.Params))
	webapi.Fail(w, webapi.ErrNoSuchAPI)
}

// dsmUIRedirect: Ziel-URL für Seitenaufrufe der DSM-Oberfläche auf den
// übernommenen Ports 5000/5001 (nur im Port-Modus, nur HTML-Seitenaufrufe).
func (s *Server) dsmUIRedirect(r *http.Request) (string, bool) {
	c := s.Cfg()
	if !c.FrontendMode || s.Up == nil || r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		return "", false
	}
	// Nur für direkte Aufrufe aus dem Heimnetz. Über einen Reverse-Proxy
	// (z.B. Nginx Proxy Manager) oder aus dem Internet ist Port 5010 meist
	// nicht erreichbar – dann DSM wie bisher durchreichen.
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("Forwarded") != "" {
		return "", false
	}
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !isLAN(net.ParseIP(h)) {
		return "", false
	}
	local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if local == nil {
		return "", false
	}
	_, lport, _ := net.SplitHostPort(local.String())
	front := map[string]bool{portOnly(c.FrontHTTP): true, portOnly(c.FrontHTTPS): true}
	if !front[lport] {
		return "", false
	}
	up, err := url.Parse(c.Upstream)
	if err != nil || up.Port() == "" {
		return "", false
	}
	httpPort, _ := strconv.Atoi(up.Port())
	scheme, port := "http", httpPort
	if r.TLS != nil {
		scheme, port = "https", httpPort+1 // DSM-HTTPS liegt neben HTTP (5010/5011)
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	return scheme + "://" + host + ":" + strconv.Itoa(port) + r.URL.RequestURI(), true
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func shortUA(ua string) string {
	if len(ua) > 48 {
		return ua[:48]
	}
	return ua
}

// clientAddr: ursprüngliche Client-Adresse (auch hinter DSM/Reverse-Proxy).
func clientAddr(r *http.Request) string { return upstream.ClientIP(r) }

func isLAN(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func portOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// maxBufferedBody: größter Rumpf, der zur API-Erkennung gepuffert wird.
const maxBufferedBody = 1 << 20

// passthrough streams a request unchanged to DSM.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request) {
	s.proxyOnce.Do(func() { s.proxy = s.Up.Proxy() })
	s.proxy.ServeHTTP(w, r)
}

func isVideoStationAPI(api string) bool {
	return strings.HasPrefix(api, "SYNO.VideoStation.") || strings.HasPrefix(api, "SYNO.VideoStation2.") ||
		strings.HasPrefix(api, "SYNO.DTV.") || strings.HasPrefix(api, "SYNO.VideoController.") ||
		strings.HasPrefix(api, "SYNO.VideoStataion.") // sic – Tippfehler existiert in der Original-Registry
}

// advertisedDefs returns the Video Station APIs reported via SYNO.API.Info.
func (s *Server) advertisedDefs() map[string]webapi.APIDef {
	out := map[string]webapi.APIDef{}
	for name, d := range s.Registry {
		if s.Cfg().Advertise == "implemented" {
			if _, ok := implTable[name]; !ok {
				continue
			}
		}
		out[name] = d
	}
	return out
}

// handleAPIInfo answers SYNO.API.Info query. Im DSM-Modus wird die echte
// DSM-Antwort geholt und um die Video-Station-Einträge ergänzt – so sehen
// Clients exakt die APIs ihrer NAS plus Video Station.
func (s *Server) handleAPIInfo(w http.ResponseWriter, req *webapi.Request) {
	if req.Method != "query" {
		webapi.Fail(w, webapi.ErrNoSuchMethod)
		return
	}
	query := req.Str("query")
	data := map[string]any{}
	if s.Up != nil {
		v := url.Values{"api": {"SYNO.API.Info"}, "version": {"1"}, "method": {"query"}, "query": {req.Params.Get("query")}}
		if m, err := s.Up.Call(req.HTTP.Context(), "entry.cgi", v); err == nil {
			if d, ok := m["data"].(map[string]any); ok {
				data = d
			}
		} else {
			log.Printf("api.info: DSM nicht erreichbar (%v) – liefere eigene Minimalantwort", err)
		}
	}
	if len(data) == 0 || s.Up == nil {
		own := map[string]webapi.APIDef{
			// Pfade/Versionen wie in einer realen DSM-7-Antwort.
			"SYNO.API.Info": {Path: "entry.cgi", MinVersion: 1, MaxVersion: 1, RequestFormat: "JSON"},
			"SYNO.API.Auth": {Path: "entry.cgi", MinVersion: 1, MaxVersion: 7},
		}
		for k, e := range webapi.InfoEntries(own, query) {
			data[k] = e
		}
	}
	defs := s.advertisedDefs()
	if ViaDSM(req.HTTP) {
		// Über DSMs Webserver: Video-Station-APIs unter dem Weiterleitungspfad
		// melden (die Apps bilden ihre URLs aus diesem „path“).
		for k, d := range defs {
			d.Path = strings.TrimPrefix(DSMPrefix, "/webapi/") + "/" + d.Path
			defs[k] = d
		}
	}
	for k, e := range webapi.InfoEntries(defs, query) {
		data[k] = e
	}
	webapi.OK(w, data)
}

func (s *Server) handleVS(w http.ResponseWriter, req *webapi.Request) {
	def, ok := s.Registry[req.API]
	if !ok {
		s.Rec.Record(req.API, req.Version, req.Method, "API nicht in Original-Registry", paramKeys(req.Params))
		webapi.Fail(w, webapi.ErrNoSuchAPI)
		return
	}
	if req.Method == "" || req.Version == 0 {
		webapi.Fail(w, webapi.ErrNoParameter)
		return
	}
	if req.Version < def.MinVersion || req.Version > def.MaxVersion {
		webapi.Fail(w, webapi.ErrVersionNotSupport)
		return
	}
	sess, code := s.Auth.Validate(req.HTTP.Context(), req.SID())
	if code != 0 {
		if s.LogCalls {
			log.Printf("vs: %s %s abgewiesen – Sitzung ungültig (Code %d)", req.API, req.Method, code)
		}
		webapi.Fail(w, code)
		return
	}
	if s.LogCalls {
		log.Printf("vs: %s v%d %s (%s) params=%v", req.API, req.Version, req.Method, sess.Account, paramKeys(req.Params))
	}
	im, ok := implTable[req.API][req.Method]
	if !ok {
		note := "nicht implementiert"
		if !def.HasMethod(req.Version, req.Method) {
			note = "Methode nicht in Original-Registry für diese Version"
		}
		s.Rec.Record(req.API, req.Version, req.Method, note, paramKeys(req.Params))
		webapi.Fail(w, webapi.ErrNoSuchMethod)
		return
	}
	blocked := s.TouchConn(sess, req.HTTP, "app")
	media := req.Method == "download" || req.Method == "stream"
	gone := s.Conns().Begin(sess.Key, media)
	defer s.Conns().End(sess.Key, media)
	im.fn(&Ctx{W: GuardWriter(w, gone), Req: req, Sess: sess, S: s, Lib: s.Lib.For(s.AccessFor(sess)), Blocked: blocked})
}

func paramKeys(v url.Values) []string {
	var out []string
	for k := range v {
		switch k {
		case "api", "method", "version", "_sid", "passwd", "SynoToken":
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ImplStatus lists implemented methods with their evidence status.
func ImplStatus() []map[string]string {
	var out []map[string]string
	for _, api := range webapi.SortedNames(implTable) {
		for _, m := range webapi.SortedNames(implTable[api]) {
			im := implTable[api][m]
			out = append(out, map[string]string{"api": api, "method": m, "status": im.status, "note": im.note})
		}
	}
	return out
}

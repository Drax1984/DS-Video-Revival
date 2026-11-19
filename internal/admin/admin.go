// Package admin is the Web Administration (Komponente H) and
// Diagnostics (Komponente I).
package admin

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/vs"
)

//go:embed web
var webFS embed.FS

// Asset liefert eine Datei der Weboberfläche (z.B. hls.js und Logo für die
// Mediathek der Jellyfin-Schnittstelle).
func Asset(name string) ([]byte, error) { return webFS.ReadFile("web/" + name) }

// Version is set by main.
var Version = "dev"

// Handler serves /dsvr/.
type Handler struct {
	S       *vs.Server
	CfgPath string
	LogPath string
	files   http.Handler
	scanCh  chan struct{}
	mu      sync.Mutex
	raw     map[string]rawToken
	// Restart startet das Programm neu (gesetzt von main; nil = nicht möglich).
	Restart func()
	// runningWebPort: Port der Weboberfläche beim Start (für den Hinweis auf
	// einen ausstehenden Neustart).
	runningWebPort int
}

// New creates the admin handler. scanCh triggers a library scan.
func New(s *vs.Server, cfgPath, logPath string, scanCh chan struct{}) *Handler {
	sub, _ := fs.Sub(webFS, "web")
	h := &Handler{S: s, CfgPath: cfgPath, LogPath: logPath, files: http.StripPrefix("/dsvr/", http.FileServer(http.FS(sub))), scanCh: scanCh, raw: map[string]rawToken{}}
	if c := s.Cfg(); c != nil {
		h.runningWebPort = portOf(c.ListenHTTP)
	}
	go func() {
		for range time.Tick(time.Hour) {
			h.gcRaw()
		}
	}()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	if r.URL.Path == "/dsvr" {
		http.Redirect(w, r, "/dsvr/", http.StatusFound)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/dsvr/media/") {
		h.serveMedia(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/dsvr/api/") {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; media-src 'self' blob:; worker-src 'self' blob:"+sameHostConnect(r))
		w.Header().Set("Cache-Control", "no-cache") // nach Updates sofort die neue Oberfläche laden
		h.files.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/dsvr/api/health" {
		// started: erkennt die Weboberfläche nach „Neu starten“ den neuen Prozess.
		writeJSON(w, map[string]any{"ok": true, "version": Version, "started": h.S.Started.Unix()})
		return
	}
	// Admin-Zugriff: sid im Header (kein Cookie → CSRF-sicher).
	sid := r.Header.Get("X-DSVR-SID")
	sess, code := h.S.Auth.Validate(r.Context(), sid)
	if code != 0 {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": "Nicht angemeldet", "code": code})
		return
	}
	setMediaCookie(w, r, sid)
	// Mediathek: alle angemeldeten DSM-Benutzer
	switch r.URL.Path {
	case "/dsvr/api/me", "/dsvr/api/library", "/dsvr/api/rate", "/dsvr/api/item", "/dsvr/api/play", "/dsvr/api/progress":
		h.S.TouchConn(sess, r, "web") // Nutzung der Mediathek (Verbindungen, „Nur eine Verbindung“)
	}
	switch r.URL.Path {
	case "/dsvr/api/me":
		var meta any
		if h.S.Meta != nil {
			meta = h.S.Meta.Status()
		}
		var blocked any
		if h.S.ConnBlocked(sess) {
			b := map[string]any{"idle_minutes": int(h.S.ConnIdle() / time.Minute)}
			if until := h.S.Conns().BannedUntil(sess.Key); !until.IsZero() {
				b["banned_until"] = until // vom Administrator getrennt und gesperrt
			} else if o, ok := h.S.Conns().Holder(sess.Key); ok {
				b["device"], b["since"], b["last_seen"] = o.Device, o.Started, o.LastSeen
			}
			blocked = b
		}
		writeJSON(w, map[string]any{"account": sess.Account, "admin": h.S.IsAppAdmin(sess), "dsm_admin": h.S.Auth.IsAdmin(sess), "version": Version,
			"tmdb": h.S.Cfg().TMDbKey != "", "meta": meta, "ffmpeg": h.S.HLS != nil && h.S.HLS.FFmpeg != "",
			"aac": h.S.HLS.CanEncodeAAC(), "h264": h.S.HLS.CanEncodeH264(), "libraries": libsOrEmpty(h.S.Cfg().Libraries),
			"blocked": blocked, "jellyfin": h.S.Jellyfin != nil && h.S.Cfg().JellyfinEnabled})
		return
	case "/dsvr/api/library":
		h.library(w, r, sess)
		return
	case "/dsvr/api/rate":
		h.rate(w, r, sess)
		return
	case "/dsvr/api/item":
		h.item(w, r, sess)
		return
	case "/dsvr/api/play":
		h.play(w, r, sess)
		return
	case "/dsvr/api/progress":
		h.progress(w, r, sess)
		return
	case "/dsvr/api/jellyfin", "/dsvr/api/jellyfin/quickconnect":
		h.jellyfin(w, r, sess)
		return
	}
	if !h.S.IsAppAdmin(sess) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"error": "Nur für Administratoren"})
		return
	}
	switch r.URL.Path {
	case "/dsvr/api/status":
		h.status(w, sess)
	case "/dsvr/api/scan":
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		select {
		case h.scanCh <- struct{}{}:
			writeJSON(w, map[string]any{"ok": true, "message": "Scan gestartet"})
		default:
			writeJSON(w, map[string]any{"ok": true, "message": "Scan bereits angefordert"})
		}
	case "/dsvr/api/unhandled":
		if r.Method == http.MethodDelete {
			h.S.Rec.Reset()
		}
		writeJSON(w, h.S.Rec.List())
	case "/dsvr/api/apistatus":
		writeJSON(w, vs.ImplStatus())
	case "/dsvr/api/config":
		h.config(w, r)
	case "/dsvr/api/log":
		h.logTail(w)
	case "/dsvr/api/settings":
		h.settings(w, r)
	case "/dsvr/api/restart":
		h.restart(w, r, sess)
	case "/dsvr/api/portmode":
		h.portmode(w, r, sid)
	case "/dsvr/api/cert":
		h.certUpload(w, r)
	case "/dsvr/api/certsync":
		h.certSync(w, r, sid)
	case "/dsvr/api/integration":
		h.integration(w, r)
	case "/dsvr/api/meta":
		h.metaAction(w, r)
	case "/dsvr/api/access":
		h.access(w, r, sid)
	case "/dsvr/api/connections":
		h.connections(w, r, sess)
	case "/dsvr/api/fsk":
		h.fsk(w, r)
	case "/dsvr/api/title":
		h.setTitle(w, r, sess)
	case "/dsvr/api/tree":
		h.tree(w, r, sess)
	case "/dsvr/api/dup-ignore":
		h.dupIgnore(w, r, sess)
	case "/dsvr/api/delete-file":
		h.deleteFile(w, r, sess, sid)
	case "/dsvr/api/delete":
		h.deleteItem(w, r, sess, sid)
	case "/dsvr/api/browse":
		h.browse(w, r, sid)
	case "/dsvr/api/grant":
		h.grant(w, r, sid)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) status(w http.ResponseWriter, sess auth.Session) {
	c := h.S.Cfg()
	type folderState struct {
		config.Folder
		Readable bool   `json:"readable"`
		Error    string `json:"error,omitempty"`
	}
	var folders []folderState
	for _, f := range c.Folders {
		fs := folderState{Folder: f}
		if _, err := os.ReadDir(f.Path); err != nil {
			fs.Error = err.Error()
		} else {
			fs.Readable = true
		}
		folders = append(folders, fs)
	}
	upstreamOK := (*bool)(nil)
	if h.S.Up != nil {
		ok := false
		if m, err := h.S.Up.Call(context.Background(), "entry.cgi", map[string][]string{"api": {"SYNO.API.Info"}, "version": {"1"}, "method": {"query"}, "query": {"SYNO.API.Auth"}}); err == nil {
			ok, _ = m["success"].(bool)
		}
		upstreamOK = &ok
	}
	writeJSON(w, map[string]any{
		"version":               Version,
		"go":                    runtime.Version(),
		"arch":                  runtime.GOOS + "/" + runtime.GOARCH,
		"uptime_s":              int(time.Since(h.S.Started).Seconds()),
		"auth_mode":             c.AuthMode,
		"upstream":              c.Upstream,
		"upstream_ok":           upstreamOK,
		"listen":                []string{c.ListenHTTP, c.ListenHTTPS},
		"user":                  sess.Account,
		"folders":               folders,
		"library":               h.S.Lib.Stats(),
		"sessions":              h.S.Auth.Sessions.Count(),
		"streams":               h.S.Streams.Count(),
		"experimental_shapes":   c.ExperimentalShapes,
		"advertise":             c.Advertise,
		"report_version":        c.ReportVersion,
		"scan_interval_minutes": c.ScanIntervalMinutes,
		"metadata":              h.S.Meta.Status(),
		"ffmpeg":                h.ffmpegPath(),
		"ffprobe":               h.S.FFprobe,
		"hls_jobs":              h.S.HLS.Count(),
		"transcode":             h.S.HLS.Diagnostics(),
	})
}

func libsOrEmpty(l []config.Library) []config.Library {
	if l == nil {
		return []config.Library{}
	}
	return l
}

// editable is the subset of settings changeable via UI.
type editable struct {
	Folders             []config.Folder  `json:"folders"`
	Libraries           []config.Library `json:"libraries"`
	ExperimentalShapes  bool             `json:"experimental_shapes"`
	Advertise           string           `json:"advertise"`
	ScanIntervalMinutes int              `json:"scan_interval_minutes"`
}

func (h *Handler) config(w http.ResponseWriter, r *http.Request) {
	c := *h.S.Cfg()
	if r.Method == http.MethodGet {
		writeJSON(w, editable{c.Folders, c.Libraries, c.ExperimentalShapes, c.Advertise, c.ScanIntervalMinutes})
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "GET/PUT", http.StatusMethodNotAllowed)
		return
	}
	var e editable
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&e); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	c.Folders, c.ExperimentalShapes, c.Advertise = e.Folders, e.ExperimentalShapes, e.Advertise
	if e.Libraries != nil {
		c.Libraries = e.Libraries
	}
	if c.Folders == nil {
		c.Folders = []config.Folder{}
	}
	if e.ScanIntervalMinutes >= 0 {
		c.ScanIntervalMinutes = e.ScanIntervalMinutes
	}
	if err := config.Save(h.CfgPath, c); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	h.S.SetCfg(c)
	log.Printf("admin: Konfiguration geändert (%d Ordner, %d eigene Bibliotheken)", len(c.Folders), len(c.Libraries))
	select {
	case h.scanCh <- struct{}{}:
	default:
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (h *Handler) logTail(w http.ResponseWriter) {
	if h.LogPath == "" {
		writeJSON(w, map[string]any{"lines": []string{"(kein Logpfad konfiguriert – Ausgabe auf stdout)"}})
		return
	}
	f, err := os.Open(h.LogPath)
	if err != nil {
		writeJSON(w, map[string]any{"lines": []string{err.Error()}})
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	const max = 64 << 10
	off := st.Size() - max
	if off < 0 {
		off = 0
	}
	_, _ = f.Seek(off, io.SeekStart)
	b, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if off > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	if len(lines) > 300 {
		lines = lines[len(lines)-300:]
	}
	writeJSON(w, map[string]any{"lines": lines})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

var reHostname = regexp.MustCompile(`^([A-Za-z0-9.-]+|\[[0-9A-Fa-f:.]+\])$`)

// sameHostConnect: „Neu starten“ mit Portwechsel fragt den neuen Port an –
// erlaubt sind Verbindungen zum selben Rechner auf beliebigem Port.
func sameHostConnect(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	if !reHostname.MatchString(host) {
		return ""
	}
	return "; connect-src 'self' http://" + host + ":* https://" + host + ":*"
}

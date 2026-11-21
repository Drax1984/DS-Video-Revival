// Package jellyfin ist eine Jellyfin-kompatible Schnittstelle (Teilmenge der
// Jellyfin-Server-API 12.x, ältere Pfade ab 10.8 eingeschlossen). Damit nutzen Jellyfin-Apps – Infuse, Swiftfin,
// Jellyfin für Android TV, Findroid, Streamyfin – die Bibliothek von DS Video
// Revival: mit denselben Benutzerrechten (Freigaben, FSK, Herunterladen, „Nur
// eine Verbindung“), Wiedergabepositionen und „Zuletzt gesehen“ wie die
// DS-video-Apps. Anmeldung mit dem DSM-Konto (Passwort wird nur geprüft, nie
// gespeichert) oder per Quick Connect (Code in der Weboberfläche bestätigen –
// auch für Konten mit Zwei-Faktor-Anmeldung).
//
// Statt der Jellyfin-Weboberfläche gibt es unter /web/ eine eigene, einfache
// Mediathek (web/): Die offizielle App „Jellyfin“ für iPhone und Android zeigt
// nur die Weboberfläche des Servers an. Nicht enthalten: Live-TV, Musik,
// Fernsteuerung (WebSocket) und Verwaltungsfunktionen.
package jellyfin

import (
	"crypto/md5"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/admin"
	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/listen"
	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/vs"
)

//go:embed web
var webFS embed.FS

// ServerVersion: gemeldete Jellyfin-Version. Aktuelle Apps (Jellyfin-SDK ab
// Herbst 2026) setzen mindestens 12.0 voraus; bedient werden die Pfade von
// 12.x und zusätzlich die älteren (/Users/{id}/Items …) von 10.8–10.11.
const ServerVersion = "12.3.0"

// ProductName: so erkennen die Apps einen Jellyfin-Server (statt Emby).
const ProductName = "Jellyfin Server"

// Handler bedient die Jellyfin-API.
type Handler struct {
	S     *vs.Server
	store *store
	qc    *quickConnect

	mu       sync.Mutex
	sessions map[string]*playSession // PlaySessionId → laufende Umwandlung

	lnMu   sync.Mutex
	ln     *listen.Manager
	lnAddr string
	disc   *discovery

	// Unbekannte Aufrufe (für spätere Erweiterungen) – je Pfadmuster einmal protokolliert.
	unknown sync.Map

	access accessLog // Wiedergabe-Abrufe (Fehlersuche)
	tr     transfers // Übertragungen beim Herunterladen (Fortschritt für die Mediathek)
}

// New creates the handler; dataDir hält jellyfin.json (Geräte, Favoriten).
func New(s *vs.Server, dataDir string, certs listen.CertSource) *Handler {
	h := &Handler{S: s, store: newStore(filepath.Join(dataDir, "jellyfin.json")), qc: newQuickConnect(), sessions: map[string]*playSession{}}
	h.ln = listen.New(h, certs)
	go func() {
		for range time.Tick(10 * time.Minute) {
			h.gcSessions()
		}
	}()
	return h
}

// Enabled: Schnittstelle in den Einstellungen eingeschaltet.
func (h *Handler) Enabled() bool { return h.S.Cfg().JellyfinEnabled }

// Apply startet/stoppt den eigenen Port und die Suche im Netz nach den Einstellungen.
func (h *Handler) Apply() {
	h.lnMu.Lock()
	defer h.lnMu.Unlock()
	cfg := h.S.Cfg()
	want := ""
	if cfg.JellyfinEnabled {
		want = ":" + strconv.Itoa(cfg.JellyfinListenPort())
	}
	if want != h.lnAddr {
		if h.lnAddr != "" {
			h.ln.Stop(h.lnAddr)
		}
		h.lnAddr = ""
		if want != "" {
			if err := h.ln.Ensure(want, false, 3*time.Second); err != nil {
				log.Printf("jellyfin: Port %s nicht verfügbar: %v", want, err)
			} else {
				h.lnAddr = want
			}
		}
	}
	if cfg.JellyfinEnabled && h.disc == nil {
		h.disc = startDiscovery(h)
	} else if !cfg.JellyfinEnabled && h.disc != nil {
		h.disc.stop()
		h.disc = nil
	}
}

// Close beendet Port, Suche im Netz und schreibt die Gerätedatei.
func (h *Handler) Close() {
	h.lnMu.Lock()
	if h.lnAddr != "" {
		h.ln.Stop(h.lnAddr)
		h.lnAddr = ""
	}
	if h.disc != nil {
		h.disc.stop()
		h.disc = nil
	}
	h.lnMu.Unlock()
	h.store.flush()
}

// Listening: eigener Port aktiv.
func (h *Handler) Listening() bool {
	h.lnMu.Lock()
	defer h.lnMu.Unlock()
	return h.lnAddr != ""
}

// ---------- Anfrage ----------

// req bündelt eine Anfrage.
type req struct {
	w    http.ResponseWriter
	r    *http.Request
	segs []string // Pfadteile, klein geschrieben
	raw  []string // Pfadteile wie gesendet
	q    map[string][]string
	auth authInfo
	dev  *device
	sess auth.Session
	view *library.View

	eps    map[int][]library.Item // Folgen je Serie (einmal je Anfrage)
	states map[int]state          // Wiedergabestand je Objekt (einmal je Anfrage)
}

// Q: Query-Parameter ohne Beachtung der Groß-/Kleinschreibung.
func (x *req) Q(name string) string {
	if v := x.q[strings.ToLower(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (x *req) QInt(name string, def int) int {
	if v, err := strconv.Atoi(x.Q(name)); err == nil {
		return v
	}
	return def
}

func (x *req) QInt64(name string, def int64) int64 {
	if v, err := strconv.ParseInt(x.Q(name), 10, 64); err == nil {
		return v
	}
	return def
}

func (x *req) QBool(name string) (bool, bool) {
	switch strings.ToLower(x.Q(name)) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// QList: kommagetrennte Liste (auch mehrfach angegebener Parameter).
func (x *req) QList(name string) []string {
	var out []string
	for _, v := range x.q[strings.ToLower(name)] {
		for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' }) {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// authInfo: Angaben aus dem Authorization-Header (MediaBrowser …).
type authInfo struct {
	Client, Device, DeviceID, Version, Token string
}

// parseAuth liest „Authorization: MediaBrowser Client="…", Device="…", …“
// (auch X-Emby-Authorization) sowie Token aus Header oder Adresse.
func parseAuth(r *http.Request, q map[string][]string) authInfo {
	var a authInfo
	for _, hv := range []string{r.Header.Get("Authorization"), r.Header.Get("X-Emby-Authorization")} {
		hv = strings.TrimSpace(hv)
		if i := strings.IndexByte(hv, ' '); i > 0 && (strings.EqualFold(hv[:i], "MediaBrowser") || strings.EqualFold(hv[:i], "Emby")) {
			hv = hv[i+1:]
		} else {
			continue
		}
		for _, part := range splitAuth(hv) {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"`)
			if u, err := url.QueryUnescape(v); err == nil {
				v = u
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "client":
				a.Client = v
			case "device":
				a.Device = v
			case "deviceid":
				a.DeviceID = v
			case "version":
				a.Version = v
			case "token":
				a.Token = v
			}
		}
		break
	}
	if a.Token == "" {
		for _, hn := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
			if v := r.Header.Get(hn); v != "" {
				a.Token = v
				break
			}
		}
	}
	if a.Token == "" {
		for _, qn := range []string{"api_key", "apikey"} {
			if v := q[qn]; len(v) > 0 && v[0] != "" {
				a.Token = v[0]
				break
			}
		}
	}
	if a.DeviceID == "" {
		if v := q["deviceid"]; len(v) > 0 {
			a.DeviceID = v[0]
		}
	}
	if a.Client == "" {
		a.Client = r.Header.Get("X-Emby-Client")
	}
	if a.Device == "" {
		a.Device = r.Header.Get("X-Emby-Device-Name")
	}
	return a
}

// splitAuth trennt an Kommas außerhalb von Anführungszeichen.
func splitAuth(s string) []string {
	var out []string
	inQ, start := false, 0
	for i, c := range s {
		switch c {
		case '"':
			inQ = !inQ
		case ',':
			if !inQ {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// match prüft Pfadteile gegen ein Muster ("users/*/items/*") und liefert die
// Platzhalter (in der gesendeten Schreibweise).
func (x *req) match(pattern string) ([]string, bool) {
	ps := strings.Split(pattern, "/")
	if len(ps) != len(x.segs) {
		return nil, false
	}
	var vars []string
	for i, p := range ps {
		if p == "*" {
			vars = append(vars, x.raw[i])
			continue
		}
		if p != x.segs[i] {
			return nil, false
		}
	}
	return vars, true
}

// ---------- ServeHTTP ----------

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-Emby-Authorization, X-Emby-Token, X-MediaBrowser-Token, Content-Type, Range")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, HEAD, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	q := map[string][]string{}
	for k, v := range r.URL.Query() {
		lk := strings.ToLower(k)
		q[lk] = append(q[lk], v...)
	}
	p := strings.Trim(r.URL.Path, "/")
	var raw []string
	if p != "" {
		raw = strings.Split(p, "/")
	}
	segs := make([]string, len(raw))
	for i, s := range raw {
		segs[i] = strings.ToLower(s)
	}
	w, done := h.trackAccess(w, r, segs)
	defer done()
	x := &req{w: w, r: r, segs: segs, raw: raw, q: q}
	x.auth = parseAuth(r, q)
	if h.public(x) {
		return
	}
	dev, ok := h.store.lookup(x.auth.Token, upstream.ClientIP(r))
	if !ok {
		w.Header().Set("WWW-Authenticate", `MediaBrowser realm="DS Video Revival"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	x.dev = dev
	x.sess = auth.Session{Account: dev.Account, IsManager: dev.IsManager, Key: dev.key(), SessionName: "Jellyfin"}
	h.S.Conns().Touch(x.sess.Key, x.sess.Account, h.S.SingleSession(x.sess),
		vs.ConnInfo{Kind: "jellyfin", Device: dev.label(), Sure: true, IP: upstream.ClientIP(r)})
	x.view = h.S.Lib.For(h.S.AccessFor(x.sess))
	h.route(x)
}

// public: Aufrufe ohne Anmeldung. true = beantwortet.
func (h *Handler) public(x *req) bool {
	switch {
	case len(x.segs) == 0 || (len(x.segs) == 1 && x.segs[0] == "index.html"):
		http.Redirect(x.w, x.r, vs.JellyfinBase(x.r)+"/web/", http.StatusFound)
	case x.segs[0] == "web":
		h.webClient(x)
	case x.is("system/info/public"):
		writeJSON(x.w, h.publicInfo(x.r))
	case x.is("system/ping"):
		writeJSON(x.w, ProductName)
	case x.is("branding/configuration"):
		writeJSON(x.w, map[string]any{"LoginDisclaimer": "", "CustomCss": "", "SplashscreenEnabled": false})
	case x.is("branding/css") || x.is("branding/css.css"):
		x.w.Header().Set("Content-Type", "text/css")
	case x.is("users/public"):
		writeJSON(x.w, []any{})
	case x.is("quickconnect/enabled"):
		writeJSON(x.w, true)
	case x.is("quickconnect/initiate"):
		h.qcInitiate(x)
	case x.is("quickconnect/connect"):
		h.qcConnect(x)
	case x.is("users/authenticatebyname"):
		h.authenticateByName(x)
	case x.is("users/authenticatewithquickconnect"):
		h.authenticateWithQuickConnect(x)
	case len(x.segs) >= 4 && x.segs[0] == "items" && x.segs[2] == "images":
		h.image(x)
	case len(x.segs) == 5 && x.segs[0] == "videos" && x.segs[2] == "hls":
		h.hlsFile(x)
	default:
		return false
	}
	return true
}

func (x *req) is(path string) bool { return strings.Join(x.segs, "/") == path }

// webClient: kleine Mediathek unter /web/ – die App „Jellyfin“ (iPhone,
// Android) zeigt nur die Weboberfläche des Servers an. Nutzt ausschließlich
// diese Schnittstelle (Anmeldung mit Token, relative Pfade).
func (h *Handler) webClient(x *req) {
	if x.r.Method != http.MethodGet && x.r.Method != http.MethodHead {
		jsonError(x.w, http.StatusMethodNotAllowed, "GET")
		return
	}
	name := strings.Join(x.raw[1:], "/")
	if name == "" && !strings.HasSuffix(x.r.URL.Path, "/") {
		http.Redirect(x.w, x.r, vs.JellyfinBase(x.r)+"/web/", http.StatusFound)
		return
	}
	var b []byte
	var err error
	ctype := ""
	switch name {
	case "", "index.html":
		b, err = webFS.ReadFile("web/index.html")
		ctype = "text/html; charset=utf-8"
		x.w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; worker-src 'self' blob:")
	case "main.dsvr.bundle.js", "app.js": // app.js: ältere, zwischengespeicherte Seiten
		b, err = webFS.ReadFile("web/app.js")
		ctype = "text/javascript; charset=utf-8"
	case "style.css":
		b, err = webFS.ReadFile("web/style.css")
		ctype = "text/css; charset=utf-8"
	case "hls.light.min.js":
		b, err = admin.Asset("hls.light.min.js")
		ctype = "text/javascript; charset=utf-8"
	case "logo.webp":
		b, err = admin.Asset("logo.webp")
		ctype = "image/webp"
	default:
		http.NotFound(x.w, x.r)
		return
	}
	if err != nil {
		http.NotFound(x.w, x.r)
		return
	}
	x.w.Header().Set("Content-Type", ctype)
	x.w.Header().Set("Cache-Control", "no-cache")
	x.w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = x.w.Write(b)
}

// publicInfo: /System/Info/Public.
func (h *Handler) publicInfo(r *http.Request) map[string]any {
	return map[string]any{
		"LocalAddress":           h.localAddress(r),
		"ServerName":             h.serverName(),
		"Version":                ServerVersion,
		"ProductName":            ProductName,
		"OperatingSystem":        "Linux",
		"Id":                     h.store.serverID(),
		"StartupWizardCompleted": true,
	}
}

func (h *Handler) serverName() string {
	host, _ := os.Hostname()
	if host == "" {
		return "DS Video Revival"
	}
	return "DS Video Revival (" + host + ")"
}

// localAddress: Adresse, unter der die Schnittstelle im Heimnetz erreichbar ist.
func (h *Handler) localAddress(r *http.Request) string {
	if base := vs.JellyfinBase(r); base != "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		return scheme + "://" + r.Host + base
	}
	ip := ""
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if host, _, err := net.SplitHostPort(la.String()); err == nil {
			ip = host
		}
	}
	if ip == "" || net.ParseIP(ip).IsLoopback() || net.ParseIP(ip).IsUnspecified() {
		ip, _, _ = net.SplitHostPort(r.Host)
		if ip == "" {
			ip = r.Host
		}
	}
	if strings.Contains(ip, ":") {
		ip = "[" + ip + "]"
	}
	return "http://" + ip + ":" + strconv.Itoa(h.S.Cfg().JellyfinListenPort())
}

// ---------- Antworten ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(v)
}

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg)
}

// readBody liest einen JSON-Rumpf (Felder ohne Beachtung der Groß-/Kleinschreibung).
func readBody(r *http.Request, v any) {
	if r.Body == nil {
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(b) == 0 {
		return
	}
	_ = json.Unmarshal(b, v)
}

// emptyResult: leeres BaseItemDtoQueryResult.
func emptyResult() map[string]any {
	return map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0}
}

// ---------- IDs ----------

// Objektarten in der ID (Jellyfin erwartet GUIDs – 32 Hexziffern).
const (
	kView   = 0x01
	kItem   = 0x02 // Film, Folge, Heimvideo, TV-Aufnahme
	kShow   = 0x03
	kSeason = 0x04
	kFile   = 0x05 // Version (MediaSource)
	kGenre  = 0x06
	kPerson = 0x07
	kRoot   = 0x08
)

const idMagic = "d5f0"

func makeID(kind byte, payload uint64) string {
	return fmt.Sprintf("%s%02x00%024x", idMagic, kind, payload)
}

// nameID: ID für Genres und Personen (aus dem Namen).
func nameID(kind byte, name string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(name))))
	return fmt.Sprintf("%s%02x00", idMagic, kind) + hex.EncodeToString(sum[:])[:24]
}

// parseID: Objektart und Nutzlast (Groß-/Kleinschreibung und Bindestriche egal).
func parseID(s string) (kind byte, payload uint64, ok bool) {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if len(s) != 32 || !strings.HasPrefix(s, idMagic) {
		return 0, 0, false
	}
	k, err := strconv.ParseUint(s[4:6], 16, 8)
	if err != nil {
		return 0, 0, false
	}
	p, err := strconv.ParseUint(s[8:], 16, 64)
	if err != nil {
		return byte(k), 0, k == kGenre || k == kPerson
	}
	return byte(k), p, true
}

func normID(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }

// userID: feste ID je Konto.
func userID(account string) string {
	sum := md5.Sum([]byte("dsvr-user:" + strings.ToLower(account)))
	return hex.EncodeToString(sum[:])
}

// Bibliothekstypen in der Ansichts-ID.
var viewTypes = []string{"", "movie", "tvshow", "home_video", "tv_record"}

func viewID(typ string, libID int) string {
	for i, t := range viewTypes {
		if t == typ && i > 0 {
			return makeID(kView, uint64(i)<<32|uint64(uint32(libID)))
		}
	}
	return ""
}

func parseViewPayload(p uint64) (typ string, libID int, ok bool) {
	i := int(p >> 32)
	if i <= 0 || i >= len(viewTypes) {
		return "", 0, false
	}
	return viewTypes[i], int(uint32(p)), true
}

func seasonID(showID, season int) string {
	return makeID(kSeason, uint64(showID)<<16|uint64(uint16(season)))
}

// ---------- Zeit ----------

const ticksPerSecond = 10_000_000

func ticks(sec float64) int64 { return int64(sec * ticksPerSecond) }

// jfTime: Zeitformat der Jellyfin-API.
func jfTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.0000000Z") }

func unixTime(sec int64) string { return jfTime(time.Unix(sec, 0)) }

// noteUnknown protokolliert einen nicht umgesetzten Aufruf (je Muster einmal).
func (h *Handler) noteUnknown(x *req) {
	pat := make([]string, len(x.segs))
	for i, s := range x.segs {
		if _, _, ok := parseID(s); ok || len(s) == 32 {
			pat[i] = "{id}"
		} else {
			pat[i] = s
		}
	}
	key := x.r.Method + " /" + strings.Join(pat, "/")
	if _, seen := h.unknown.LoadOrStore(key, true); !seen {
		log.Printf("jellyfin: nicht umgesetzt: %s (%s)", key, x.dev.label())
	}
}

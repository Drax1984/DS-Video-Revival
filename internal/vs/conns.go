package vs

// Aktive Verbindungen (Weboberfläche → Verbindungen) und „Nur eine
// Verbindung gleichzeitig“ (Benutzerrechte).
//
// Eine Verbindung ist eine Sitzung (sid) – je Gerät bzw. Browser eine.
// Aktivität: jeder Aufruf der Video-Station-API, der Mediathek der
// Weboberfläche und jede laufende Wiedergabe oder jeder Download.
// Für eingeschränkte Konten hat die erste aktive Verbindung den Platz; jede
// weitere sieht keine Medien, bis dort die Ruhezeit lang nichts mehr lief
// (Benutzerrechte, Standard ConnIdle) oder sie abgemeldet bzw. getrennt wurde.
// „Trennen“ kann das Gerät (Konto + Gerät + Adresse) zusätzlich für eine Weile
// sperren – sonst meldet sich eine App mit gespeichertem Passwort gleich neu
// an. Nur im Speicher: nach einem Neustart bekommt die erste Verbindung, die
// sich meldet, den Platz, und Sperren entfallen.

import (
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/library"
)

const (
	// ConnIdle: ohne Anfrage und ohne laufende Wiedergabe gilt eine
	// Verbindung danach als ruhend – ihr Platz wird frei (Standard; in den
	// Benutzerrechten einstellbar).
	ConnIdle = 5 * time.Minute
	// connListFor: so lange nach der letzten Aktivität erscheint eine
	// Verbindung in der Liste der Weboberfläche.
	connListFor = 30 * time.Minute
	// connForget: danach wird ein Eintrag ohne laufende Anfrage entfernt.
	connForget = 24 * time.Hour
)

// Conn: eine Verbindung (Sitzung).
type Conn struct {
	Key      string // SHA-256 der sid
	Account  string
	Kind     string // app | web
	Device   string
	IP       string
	Started  time.Time
	LastSeen time.Time
	StreamAt time.Time // letzte Wiedergabe-/Download-Aktivität
	Title    string    // zuletzt gestarteter Titel
	open     int       // laufende Anfragen
	streams  int       // laufende Wiedergaben/Downloads
	single   bool      // Konto darf nur eine Verbindung nutzen
	gone     *atomic.Bool
	logged   time.Time // letzte Meldung „zweite Verbindung“
	banned   time.Time // Gerät gesperrt bis (vom Administrator getrennt)
}

func (r *Conns) idle() time.Duration {
	if r.Idle != nil {
		if d := r.Idle(); d > 0 {
			return d
		}
	}
	return ConnIdle
}

func (r *Conns) active(c *Conn, now time.Time) bool {
	return c.open > 0 || now.Sub(c.LastSeen) < r.idle()
}

// Ban: gesperrtes Gerät (Konto + Gerät + Adresse).
type Ban struct {
	ID      string    `json:"id"`
	Account string    `json:"account"`
	Device  string    `json:"device"`
	IP      string    `json:"ip"`
	Until   time.Time `json:"until"`
}

func (b Ban) matches(c *Conn) bool {
	return strings.EqualFold(b.Account, c.Account) && b.IP == c.IP && (b.Device == "" || b.Device == c.Device)
}

// banLocked: Sperre, die auf die Verbindung passt (Ende, 0 = keine).
func (r *Conns) banLocked(c *Conn, now time.Time) time.Time {
	var until time.Time
	for _, b := range r.bans {
		if now.Before(b.Until) && b.matches(c) && b.Until.After(until) {
			until = b.Until
		}
	}
	return until
}

// Conns verwaltet die Verbindungen.
type Conns struct {
	mu     sync.Mutex
	m      map[string]*Conn
	holder map[string]string // Konto (klein) → Verbindung mit dem Platz
	jobs   map[string]string // HLS-Job bzw. Wiedergabe-Token → Verbindung
	bans   map[string]Ban
	// Alive: besteht die Sitzung noch (nicht abgemeldet/abgelaufen)?
	Alive func(key string) bool
	// Idle: Ruhezeit, nach der der Platz frei wird (nil/0 = ConnIdle).
	Idle func() time.Duration
	last time.Time // letzte Aufräumrunde
}

// NewConns legt die Verwaltung an.
func NewConns() *Conns {
	return &Conns{m: map[string]*Conn{}, holder: map[string]string{}, jobs: map[string]string{}, bans: map[string]Ban{}}
}

// ConnInfo: Angaben zum Gerät.
type ConnInfo struct {
	Kind, Device, IP string
	Sure             bool // Device stammt von der App/dem Browser selbst
}

// Touch meldet Aktivität einer Sitzung. single: das Konto darf nur eine
// Verbindung nutzen. Ergebnis: true = andere Verbindung hat den Platz.
func (r *Conns) Touch(key, account string, single bool, ci ConnInfo) bool {
	if key == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.gcLocked(now)
	c := r.m[key]
	if c == nil {
		c = &Conn{Key: key, Started: now, gone: &atomic.Bool{}}
		r.m[key] = c
	}
	c.Account, c.LastSeen, c.single = account, now, single
	if ci.Kind != "" {
		c.Kind = ci.Kind
	}
	if ci.Device != "" && (c.Device == "" || ci.Sure) {
		c.Device = ci.Device
	}
	if ci.IP != "" {
		c.IP = ci.IP
	}
	if r.bannedLocked(c, now) {
		return true
	}
	return r.slotLocked(c, now)
}

// bannedLocked: Gerät gesperrt? Gibt dann auch den Platz frei.
func (r *Conns) bannedLocked(c *Conn, now time.Time) bool {
	until := r.banLocked(c, now)
	c.banned = until
	if until.IsZero() {
		return false
	}
	if a := strings.ToLower(c.Account); r.holder[a] == c.Key {
		delete(r.holder, a)
	}
	if now.Sub(c.logged) > 10*time.Minute {
		c.logged = now
		log.Printf("verbindungen: %q auf %s ist bis %s gesperrt (vom Administrator getrennt) – keine Medien", c.Account, c.label(), until.Format("15:04"))
	}
	return true
}

// slotLocked: Platzvergabe; true = gesperrt (zweite Verbindung).
func (r *Conns) slotLocked(c *Conn, now time.Time) bool {
	acct := strings.ToLower(c.Account)
	if !c.single {
		if r.holder[acct] == c.Key {
			delete(r.holder, acct)
		}
		return false
	}
	h := r.holder[acct]
	if h == "" || h == c.Key {
		r.holder[acct] = c.Key
		return false
	}
	hc := r.m[h]
	gone := r.Alive != nil && !r.Alive(h)
	if hc == nil || gone || !r.active(hc, now) || !hc.single || hc.banned.After(now) {
		if hc != nil {
			why := "ruhte seit " + now.Sub(hc.LastSeen).Round(time.Second).String()
			if gone {
				why = "abgemeldet"
			}
			log.Printf("verbindungen: %q – jetzt %s (vorher %s, %s)", c.Account, c.label(), hc.label(), why)
		}
		r.holder[acct] = c.Key
		return false
	}
	if now.Sub(c.logged) > 10*time.Minute {
		c.logged = now
		log.Printf("verbindungen: %q ist schon auf %s aktiv – %s sieht keine Medien (Benutzerrechte → Nur eine Verbindung)", c.Account, hc.label(), c.label())
	}
	return true
}

func (c *Conn) label() string {
	s := c.Device
	if s == "" {
		s = map[string]string{"app": "App", "web": "Weboberfläche"}[c.Kind]
	}
	if c.IP != "" {
		s += " (" + c.IP + ")"
	}
	return s
}

// Blocked: hat gerade eine andere Verbindung desselben Kontos den Platz?
func (r *Conns) Blocked(key string) bool {
	if key == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.m[key]
	if c == nil {
		return false
	}
	now := time.Now()
	if c.banned.After(now) {
		return true
	}
	if !c.single {
		return false
	}
	return r.blockedLocked(c, now)
}

// BannedUntil: Ende der Sperre der Verbindung (0 = nicht gesperrt).
func (r *Conns) BannedUntil(key string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.m[key]; c != nil && c.banned.After(time.Now()) {
		return c.banned
	}
	return time.Time{}
}

// Holder: Verbindung, die für das Konto der Sitzung key den Platz hat.
func (r *Conns) Holder(key string) (Conn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.m[key]
	if c == nil {
		return Conn{}, false
	}
	hc := r.m[r.holder[strings.ToLower(c.Account)]]
	if hc == nil || hc.Key == key {
		return Conn{}, false
	}
	return *hc, true
}

// Begin/End: laufende Anfrage (hält die Verbindung aktiv). stream: Wiedergabe
// oder Download. Ergebnis von Begin: Merker „getrennt“ für den Abbruch.
func (r *Conns) Begin(key string, stream bool) *atomic.Bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.m[key]
	if c == nil {
		return &atomic.Bool{}
	}
	c.open++
	now := time.Now()
	c.LastSeen = now
	if stream {
		c.streams++
		c.StreamAt = now
	}
	return c.gone
}

// End beendet eine mit Begin gemeldete Anfrage.
func (r *Conns) End(key string, stream bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.m[key]
	if c == nil {
		return
	}
	now := time.Now()
	c.LastSeen = now
	if c.open > 0 {
		c.open--
	}
	if stream {
		c.StreamAt = now
		if c.streams > 0 {
			c.streams--
		}
	}
}

// Stream meldet Wiedergabe über einen Job bzw. ein Token (HLS-Segmente,
// Direktwiedergabe im Browser). Ergebnis: Verbindung (Schlüssel), gesperrt.
func (r *Conns) Stream(job string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.jobs[job]
	if !ok {
		return "", false
	}
	c := r.m[key]
	if c == nil {
		return key, false
	}
	now := time.Now()
	c.LastSeen, c.StreamAt = now, now
	if r.bannedLocked(c, now) {
		return key, true
	}
	return key, r.slotLocked(c, now)
}

// Play merkt Titel und Job (HLS) bzw. Token einer gestarteten Wiedergabe.
func (r *Conns) Play(key, job, title string) {
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if job != "" {
		r.jobs[job] = key
	}
	if c := r.m[key]; c != nil {
		if title != "" {
			c.Title = title
		}
		c.StreamAt = time.Now()
	}
}

// Kick trennt eine Verbindung: Eintrag weg, Platz frei, laufende Anfragen
// werden abgebrochen; ban > 0 sperrt das Gerät (Konto + Gerät + Adresse) so
// lange. Ergebnis: Daten der Verbindung und ihre Jobs/Token.
func (r *Conns) Kick(key string, ban time.Duration) (Conn, []string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.m[key]
	if c == nil {
		return Conn{}, nil, false
	}
	if ban > 0 {
		b := Ban{Account: strings.ToLower(c.Account), Device: c.Device, IP: c.IP, Until: time.Now().Add(ban)}
		b.ID = auth.HashSID(b.Account + "\x00" + b.Device + "\x00" + b.IP)[:16]
		if old, ok := r.bans[b.ID]; ok && old.Until.After(b.Until) {
			b.Until = old.Until
		}
		r.bans[b.ID] = b
	}
	c.gone.Store(true)
	delete(r.m, key)
	acct := strings.ToLower(c.Account)
	if r.holder[acct] == key {
		delete(r.holder, acct)
	}
	var jobs []string
	for j, k := range r.jobs {
		if k == key {
			jobs = append(jobs, j)
			delete(r.jobs, j)
		}
	}
	return *c, jobs, true
}

// Bans: aktive Gerätesperren, die längste zuerst.
func (r *Conns) Bans() []Ban {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := []Ban{}
	for id, b := range r.bans {
		if !now.Before(b.Until) {
			delete(r.bans, id)
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.After(out[j].Until) })
	return out
}

// Unban hebt eine Gerätesperre auf.
func (r *Conns) Unban(id string) (Ban, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.bans[id]
	delete(r.bans, id)
	for _, c := range r.m {
		if ok && b.matches(c) {
			c.banned = time.Time{}
		}
	}
	return b, ok
}

// ConnView: Eintrag der Liste in der Weboberfläche.
type ConnView struct {
	ID       string    `json:"id"`
	Account  string    `json:"account"`
	Kind     string    `json:"kind"`
	Device   string    `json:"device"`
	IP       string    `json:"ip"`
	Started  time.Time `json:"started"`
	LastSeen time.Time `json:"last_seen"`
	Title    string    `json:"title,omitempty"`
	State    string    `json:"state"` // play | active | idle | blocked | banned
	Single   bool      `json:"single"`
	Until    time.Time `json:"until,omitzero"` // gesperrt bis (banned)
}

// List: Verbindungen der letzten connListFor, neueste Aktivität zuerst.
func (r *Conns) List() []ConnView {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.gcLocked(now)
	out := []ConnView{}
	for _, c := range r.m {
		if c.open == 0 && now.Sub(c.LastSeen) > connListFor {
			continue
		}
		if r.Alive != nil && !r.Alive(c.Key) {
			continue
		}
		v := ConnView{ID: c.Key, Account: c.Account, Kind: c.Kind, Device: c.Device, IP: c.IP, Started: c.Started,
			LastSeen: c.LastSeen, Title: c.Title, Single: c.single}
		switch {
		case c.banned.After(now):
			v.State, v.Until = "banned", c.banned
		case c.single && r.holder[strings.ToLower(c.Account)] != c.Key && r.blockedLocked(c, now):
			v.State = "blocked"
		case c.streams > 0 || now.Sub(c.StreamAt) < time.Minute:
			v.State = "play"
		case r.active(c, now):
			v.State = "active"
		default:
			v.State = "idle"
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (r *Conns) blockedLocked(c *Conn, now time.Time) bool {
	hc := r.m[r.holder[strings.ToLower(c.Account)]]
	return hc != nil && hc.Key != c.Key && hc.single && r.active(hc, now) && !hc.banned.After(now) && (r.Alive == nil || r.Alive(hc.Key))
}

func (r *Conns) gcLocked(now time.Time) {
	if now.Sub(r.last) < time.Minute {
		return
	}
	r.last = now
	for k, c := range r.m {
		if c.open == 0 && (now.Sub(c.LastSeen) > connForget || (r.Alive != nil && !r.Alive(k))) {
			delete(r.m, k)
			if a := strings.ToLower(c.Account); r.holder[a] == k {
				delete(r.holder, a)
			}
		}
	}
	for j, k := range r.jobs {
		if _, ok := r.m[k]; !ok {
			delete(r.jobs, j)
		}
	}
	for id, b := range r.bans {
		if !now.Before(b.Until) {
			delete(r.bans, id)
		}
	}
}

// ---------- Anbindung an den Server ----------

// Conns: Verbindungsverwaltung des Servers (bei Bedarf angelegt).
func (s *Server) Conns() *Conns {
	s.connsOnce.Do(func() {
		s.conns = NewConns()
		if (s.Auth != nil && s.Auth.Sessions != nil) || s.Jellyfin != nil {
			s.conns.Alive = s.keyAlive
		}
		s.conns.Idle = s.ConnIdle
	})
	return s.conns
}

// ConnIdle: Ruhezeit für „Nur eine Verbindung“ (Benutzerrechte, Standard 5 Minuten).
func (s *Server) ConnIdle() time.Duration {
	if m := s.Cfg().SingleSessionIdle; m > 0 {
		return time.Duration(m) * time.Minute
	}
	return ConnIdle
}

// SingleSession: darf das Konto nur eine Verbindung gleichzeitig nutzen?
// Administratoren sind ausgenommen (Aussperrschutz).
func (s *Server) SingleSession(sess auth.Session) bool {
	cfg := s.Cfg()
	if len(cfg.SingleSession) == 0 || !knownAccount(sess.Account) || s.IsAppAdmin(sess) {
		return false
	}
	keys := principalKeys(cfg, strings.ToLower(sess.Account))
	for _, k := range cfg.SingleSession {
		if keys[strings.ToLower(k)] {
			return true
		}
	}
	return false
}

// TouchConn meldet Aktivität der Sitzung (kind: app | web).
// Ergebnis: true = zweite Verbindung eines eingeschränkten Kontos.
func (s *Server) TouchConn(sess auth.Session, r *http.Request, kind string) bool {
	dev, sure := DeviceLabel(r.UserAgent())
	return s.Conns().Touch(sess.Key, sess.Account, s.SingleSession(sess), ConnInfo{Kind: kind, Device: dev, Sure: sure, IP: clientAddr(r)})
}

// ConnBlocked: Sitzung ist die zweite Verbindung eines eingeschränkten Kontos.
func (s *Server) ConnBlocked(sess auth.Session) bool { return s.Conns().Blocked(sess.Key) }

// denyAll: Sicht ohne Medien (zweite Verbindung).
var denyAll = library.Access{Folders: []string{}, MaxFSK: -1}

// connWriter bricht die Antwort ab, sobald die Verbindung getrennt wurde.
type connWriter struct {
	http.ResponseWriter
	gone *atomic.Bool
}

func (w connWriter) Write(b []byte) (int, error) {
	if w.gone.Load() {
		return 0, http.ErrAbortHandler
	}
	return w.ResponseWriter.Write(b)
}

func (w connWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w connWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// GuardWriter: Antwort, die beim Trennen abbricht.
func GuardWriter(w http.ResponseWriter, gone *atomic.Bool) http.ResponseWriter {
	return connWriter{ResponseWriter: w, gone: gone}
}

var (
	reDalvik = regexp.MustCompile(`Android [0-9.]+; ([^;)]+?)(?: Build/[^;)]*)?\)`)
	reAppVer = regexp.MustCompile(`DS(?: |%20)?video[ /]([0-9.]+)`)
)

// DeviceLabel: Gerät aus dem User-Agent, z.B. „Android TV · TV-X100“,
// „iOS“, „Browser · Chrome (Windows)“. sure: Angabe der App bzw. des Browsers
// selbst (der Videoplayer der Apps meldet sich oft nur allgemein).
func DeviceLabel(ua string) (label string, sure bool) {
	if ua == "" {
		return "", false
	}
	low := strings.ToLower(ua)
	model := ""
	if m := reDalvik.FindStringSubmatch(ua); m != nil {
		model = strings.TrimSpace(m[1])
	}
	with := func(s string) string {
		if model != "" {
			return s + " · " + model
		}
		return s
	}
	isApp := reAppVer.MatchString(ua) || strings.Contains(low, "ds%20video") || strings.Contains(low, "dsvideo")
	switch {
	case strings.HasPrefix(ua, "TV DS video") || strings.Contains(low, "android tv"):
		return with("Android TV"), true
	case strings.Contains(ua, "Dalvik") || strings.Contains(low, "okhttp"):
		return with("Android"), isApp
	case isApp && (strings.Contains(ua, "Darwin") || strings.Contains(ua, "CFNetwork") || strings.Contains(ua, "iOS") || strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad")):
		return "iOS", true
	case strings.HasPrefix(ua, "AppleCoreMedia"):
		return "iOS (Player)", false
	case strings.Contains(ua, "Mozilla/"):
		b := "Browser"
		switch {
		case strings.Contains(ua, "Edg/"):
			b = "Edge"
		case strings.Contains(ua, "OPR/"):
			b = "Opera"
		case strings.Contains(ua, "Firefox/"):
			b = "Firefox"
		case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
			b = "Chrome"
		case strings.Contains(ua, "Safari/"):
			b = "Safari"
		}
		sys := ""
		switch {
		case strings.Contains(ua, "iPhone"):
			sys = "iPhone"
		case strings.Contains(ua, "iPad"):
			sys = "iPad"
		case strings.Contains(ua, "Android"):
			sys = "Android"
		case strings.Contains(ua, "Windows"):
			sys = "Windows"
		case strings.Contains(ua, "Mac OS X"):
			sys = "Mac"
		case strings.Contains(ua, "Linux"):
			sys = "Linux"
		}
		if sys != "" {
			return "Browser · " + b + " (" + sys + ")", true
		}
		return "Browser · " + b, true
	case isApp:
		return "DS video", true
	}
	if len(ua) > 40 {
		ua = ua[:40] + "…"
	}
	return ua, false
}

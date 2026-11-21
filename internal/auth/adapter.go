package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/webapi"
)

// Adapter is the Authentication Adapter (Komponente C).
type Adapter struct {
	prefMu   sync.Mutex
	prefSess map[string]string // Konto → zuletzt erfolgreicher DSM-Sitzungsname
	// PrefFile: dort bleibt prefSess über Neustarts erhalten (spart bei der
	// ersten Anmeldung einen abgelehnten DSM-Anmeldeversuch).
	PrefFile   string
	prefLoaded bool
	Mode       string // "dsm" | "local"
	Upstream   *upstream.Client
	Local      *LocalUsers
	Sessions   *Store
	Limiter    *RateLimiter
	AdminUsers map[string]bool
	// RevalidateAfter: wie lange eine DSM-Validierung gültig bleibt.
	RevalidateAfter time.Duration
}

// NewAdapter wires the adapter.
func NewAdapter(mode string, up *upstream.Client, local *LocalUsers, store *Store, admins []string) *Adapter {
	am := map[string]bool{}
	for _, a := range admins {
		am[strings.ToLower(a)] = true
	}
	return &Adapter{Mode: mode, Upstream: up, Local: local, Sessions: store,
		Limiter: NewRateLimiter(5, 10*time.Minute), AdminUsers: am, RevalidateAfter: 2 * time.Minute}
}

// HandleAuth processes a SYNO.API.Auth request.
func (a *Adapter) HandleAuth(w http.ResponseWriter, req *webapi.Request, rawBody []byte) {
	ip := remoteIP(req.HTTP)
	method := strings.ToLower(req.Method)
	t0 := time.Now()
	if method == "login" && a.Limiter.Blocked(ip) {
		log.Printf("auth: login von %s blockiert (zu viele Fehlversuche)", ip)
		time.Sleep(time.Second)
		webapi.Fail(w, webapi.ErrAuthBadCredentials)
		return
	}
	if a.Mode == "local" {
		a.handleLocal(w, req, ip)
		return
	}
	account := req.Str("account")
	sidBefore := req.SID()
	sessName := req.Str("session")
	// Weboberfläche des Pakets (session=DSVideoRevival, sid im Header): DSMs
	// Cookies weder mitschicken noch übernehmen. Sonst ersetzte DSMs Antwort
	// beim Anmelden das Sitzungs-Cookie „id“ der DSM-Oberfläche im Browser
	// (Cookies gelten portübergreifend für den Host) – und das Abmelden hier
	// beendete dann auch die DSM-Sitzung.
	if sessName == "DSVideoRevival" && (method == "login" || method == "logout") {
		req.HTTP.Header.Del("Cookie")
		w = &noCookieWriter{ResponseWriter: w}
	}
	// DSM prüft bei session=VideoStation die Anwendungsberechtigung der (nicht
	// installierten) Video Station → normale Benutzer erhalten 402. Deshalb als
	// eigene Sitzung des Pakets anmelden. Die Apps verschlüsseln nur account
	// und passwd; „session“ steht im Klartext (DS video Android: DoLoginTask).
	if method == "login" {
		if dec := decryptLogin(req.Params); dec != nil {
			// Mit dem Paketschlüssel verschlüsselt: Klartext-Anmeldung an DSM
			// über die lokale Verbindung, verschlüsselte Felder ersetzt.
			v := url.Values{}
			for k, vs := range req.Params {
				if k != cipherKey {
					v[k] = vs
				}
			}
			for k, vs := range dec {
				v[k] = vs
			}
			account = v.Get("account")
			sessName = v.Get("session")
			rawBody = []byte(v.Encode())
			req.HTTP.URL.RawQuery = ""
			req.HTTP.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.HTTP.ContentLength = int64(len(rawBody))
			req.Params = v
		}
	}
	// Sitzungsname für DSM: Normale Benutzer erhalten bei „VideoStation“
	// (Video Station fehlt) und bei unbekannten Namen wie „DSVideoRevival“ von
	// DSM 402. „webui“ ist die DSM-eigene Sitzung (so auch N4S4/synology-api:
	// „Hardcoded for handle non administrator users API usage“); als letzte
	// Möglichkeit „FileStation“ (Benutzer mit File-Station-, aber ohne DSM-Recht).
	// Was bei einem Konto geklappt hat, wird beim nächsten Mal zuerst versucht.
	var retry []string
	used := sessName
	if method == "login" && (strings.EqualFold(sessName, "VideoStation") || sessName == "DSVideoRevival") {
		cands := a.sessionOrder(account)
		switch {
		case strings.EqualFold(sessName, "VideoStation"):
			used = cands[0]
			rawBody = rewriteSession(req, rawBody, sessName, used)
			retry = cands[1:]
		case a.prefFor(account) != "":
			// Weboberfläche: Der eigene Name „DSVideoRevival“ wurde für dieses
			// Konto schon einmal abgelehnt – gleich den bewährten nehmen. Jeder
			// abgelehnte Versuch ist eine vollständige DSM-Anmeldung (langsam).
			used = cands[0]
			rawBody = rewriteSession(req, rawBody, sessName, used)
			retry = cands[1:]
		default:
			retry = cands
		}
	}
	// Weboberfläche: nie eine SynoToken-Sitzung anlegen (DSM verlangt den Token
	// sonst bei jedem Aufruf, die Sitzungsprüfung schlüge mit 119 fehl).
	// Betrifft ältere, im Browser zwischengespeicherte Oberflächen (0.3.5).
	if method == "login" && req.Str("session") == "DSVideoRevival" && req.Params.Get("enable_syno_token") != "" &&
		strings.HasPrefix(req.HTTP.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if v, err := url.ParseQuery(string(rawBody)); err == nil {
			v.Del("enable_syno_token")
			rawBody = []byte(v.Encode())
			req.HTTP.ContentLength = int64(len(rawBody))
		}
	}
	a.Upstream.ForwardFilter(w, req.HTTP, rawBody, func(m map[string]any) []byte {
		ok, _ := m["success"].(bool)
		replaced := false
		if method == "login" && !ok && errCode(m) == 402 && len(retry) > 0 {
			if base, err := url.ParseQuery(string(rawBody)); err == nil {
				for k, vs := range req.HTTP.URL.Query() {
					if base.Get(k) == "" {
						base[k] = vs
					}
				}
				for _, alt := range retry {
					if alt == noSession {
						base.Del("session")
					} else {
						base.Set("session", alt)
					}
					m2, err := a.Upstream.Call(req.HTTP.Context(), "entry.cgi", base)
					if err != nil {
						break
					}
					if ok2, _ := m2["success"].(bool); ok2 {
						log.Printf("auth: DSM verweigert Sitzung „%s“, Anmeldung als „%s“ erfolgreich", used, alt)
						m, ok, replaced, used = m2, true, true, alt
						break
					}
					if errCode(m2) != 402 {
						m = m2
						replaced = true
						break
					}
				}
			}
		}
		switch method {
		case "login":
			if !ok {
				// Nur echte Fehlversuche zählen (400 falsches Passwort, 404 falscher
				// 2FA-Code) – nicht 402 (Berechtigung) oder 403 (2FA-Code nötig).
				if e, _ := m["error"].(map[string]any); e != nil {
					if c, _ := e["code"].(float64); c == 400 || c == 404 {
						a.Limiter.Fail(ip)
					}
				}
				log.Printf("auth: DSM-Anmeldung fehlgeschlagen für %q von %s (%v)", account, ip, m["error"])
				if replaced {
					b, _ := json.Marshal(m)
					return b
				}
				return nil
			}
			a.Limiter.Success(ip)
			data, _ := m["data"].(map[string]any)
			sid, _ := data["sid"].(string)
			if sid == "" {
				// format=cookie: sid steht nur im Set-Cookie (wird unverändert weitergereicht).
				sid = cookieSID(w.Header())
			}
			if sid != "" {
				info := a.sessionInfo(req.HTTP.Context(), sid)
				if info.User != "" {
					account = info.User // auch bei verschlüsselter Anmeldung (Android)
				}
				if info.Priv != nil && !*info.Priv && !a.isAdminInfo(account, info) {
					log.Printf("auth: %q von %s abgelehnt – keine Anwendungsberechtigung für DS Video Revival (DSM → Systemsteuerung → Anwendungsberechtigungen)", account, ip)
					a.logoutUpstream(sid)
					// 402 = „Permission denied“ (SYNO.API.Auth, offizieller API-Leitfaden)
					return []byte(`{"error":{"code":402},"success":false}`)
				}
				a.Sessions.Put(sid, Session{Account: account, SessionName: sessName, PrivChecked: time.Now()})
				// Gerade von DSM angemeldet: gilt als geprüft (keine sofortige
				// Rückfrage) – sofern DSM die Rolle (Admin ja/nein) genannt hat.
				if info.IsAdmin != nil {
					a.Sessions.MarkValidated(sid, info.IsAdmin)
				}
				a.rememberSession(account, used)
				log.Printf("auth: %q angemeldet über DSM (Sitzung %s%s, %.1f s)", account, used, info.note(), time.Since(t0).Seconds())
			}
		case "logout":
			if sidBefore != "" {
				a.Sessions.Delete(sidBefore)
			}
		}
		if replaced {
			// Die Android-App führt die Sitzung über das Cookie „id“ (OkHttp,
			// WebAPIRequest sendet _sid nur bei externen Links). Bei einer
			// Ersatzanmeldung fehlt DSMs Set-Cookie – daher selbst setzen.
			if d, _ := m["data"].(map[string]any); d != nil {
				if sid, _ := d["sid"].(string); sid != "" && req.Str("format") != "sid" {
					http.SetCookie(w, &http.Cookie{Name: "id", Value: sid, Path: "/", HttpOnly: true})
				}
			}
			b, _ := json.Marshal(m)
			return b
		}
		return nil
	})
}

func errCode(m map[string]any) int {
	if e, _ := m["error"].(map[string]any); e != nil {
		c, _ := e["code"].(float64)
		return int(c)
	}
	return 0
}

func (a *Adapter) handleLocal(w http.ResponseWriter, req *webapi.Request, ip string) {
	switch strings.ToLower(req.Method) {
	case "login":
		u, err := a.Local.Check(req.Str("account"), req.Params.Get("passwd"))
		if err != nil {
			a.Limiter.Fail(ip)
			if err == ErrDisabled {
				webapi.Fail(w, webapi.ErrAuthDisabled)
				return
			}
			webapi.Fail(w, webapi.ErrAuthBadCredentials)
			return
		}
		a.Limiter.Success(ip)
		sid := NewSID()
		mgr := u.IsManager
		a.Sessions.Put(sid, Session{Account: u.Account, SessionName: req.Str("session"), IsManager: &mgr})
		if req.Str("format") != "sid" {
			http.SetCookie(w, &http.Cookie{Name: "id", Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		// Antwortfelder belegt durch Schema SYNO.API.Auth-login (did, is_portal_port, sid).
		webapi.OK(w, map[string]any{"did": "", "is_portal_port": false, "sid": sid})
	case "logout":
		if sid := req.SID(); sid != "" {
			a.Sessions.Delete(sid)
		}
		webapi.OK(w, nil)
	default:
		webapi.Fail(w, webapi.ErrNoSuchMethod)
	}
}

// Validate resolves a sid to a session. In DSM mode the sid is periodically
// re-checked upstream so that a DSM logout/expiry is honoured.
func (a *Adapter) Validate(ctx context.Context, sid string) (Session, int) {
	if sid == "" || a.Sessions.IsRevoked(sid) {
		return Session{}, webapi.ErrSIDNotFound
	}
	sess, ok := a.Sessions.Get(sid)
	if a.Mode == "local" {
		if !ok {
			return Session{}, webapi.ErrSIDNotFound
		}
		return sess, 0
	}
	if ok && !sess.Validated.IsZero() && time.Since(sess.Validated) < a.RevalidateAfter {
		return sess, 0
	}
	// Anwendungsberechtigung regelmäßig neu prüfen (Änderungen in DSM wirken
	// spätestens nach PrivRecheck).
	if ok && time.Since(sess.PrivChecked) > PrivRecheck {
		info := a.sessionInfo(ctx, sid)
		if info.Priv != nil && !*info.Priv && !a.isAdminInfo(sess.Account, info) {
			log.Printf("auth: Sitzung von %q beendet – Anwendungsberechtigung entzogen", sess.Account)
			a.Sessions.Delete(sid)
			return Session{}, webapi.ErrSIDNotFound
		}
		if info.User != "" && sess.Account != info.User {
			sess.Account = info.User
		}
		sess.PrivChecked = time.Now()
		a.Sessions.Put(sid, sess)
	}
	valid, known, isMgr, user, why := a.checkUpstream(ctx, sid)
	if known && !valid {
		log.Printf("auth: DSM hat die Sitzung von %q beendet (%s)", sess.Account, why)
		a.Sessions.Delete(sid)
		return Session{}, webapi.ErrSIDNotFound
	}
	if !known {
		if !ok {
			return Session{}, webapi.ErrSIDNotFound
		}
		// Nicht entscheidbar (z.B. DSM kurz nicht erreichbar, oder die
		// Prüf-APIs sind für diese Sitzungsart gesperrt): Sitzung behalten.
		log.Printf("auth: Sitzung von %q nicht prüfbar (%s) – bleibt gültig", sess.Account, why)
	}
	if !ok {
		info := a.sessionInfo(ctx, sid)
		if info.Priv != nil && !*info.Priv && !a.isAdminInfo(info.User, info) {
			return Session{}, webapi.ErrSIDNotFound
		}
		if info.User != "" {
			user = info.User
		}
		if user == "" {
			// sid gültig, aber Konto unbekannt (z.B. Anmeldung nicht über diesen Dienst).
			user = "?"
		}
		a.Sessions.Put(sid, Session{Account: user, IsManager: isMgr, PrivChecked: time.Now()})
		a.Sessions.MarkValidated(sid, isMgr)
		sess, _ = a.Sessions.Get(sid)
		return sess, 0
	}
	a.Sessions.MarkValidated(sid, isMgr)
	sess, _ = a.Sessions.Get(sid)
	return sess, 0
}

// checkUpstream prüft eine DSM-sid. Ergebnis: valid/known – „known“ ist nur
// gesetzt, wenn DSM eindeutig antwortet. Ungültig sind nur 106 (Zeitüberschreitung),
// 107 (doppelte Anmeldung) und 119 (sid unbekannt); 105 (keine Berechtigung)
// heißt, DSM kennt die Sitzung.
func (a *Adapter) checkUpstream(ctx context.Context, sid string) (valid, known bool, mgr *bool, user, why string) {
	var notes []string
	// Initdata zuerst: funktioniert nachweislich für alle Sitzungsarten des
	// Pakets (auch ohne Sitzungsnamen und ohne File-Station-Recht).
	for _, api := range []string{"SYNO.Core.Desktop.Initdata", "SYNO.FileStation.Info", "SYNO.Core.NormalUser"} {
		ver := "1"
		if api == "SYNO.FileStation.Info" {
			ver = "2"
		}
		m, err := a.Upstream.Call(ctx, "entry.cgi", url.Values{"api": {api}, "version": {ver}, "method": {"get"}, "_sid": {sid}})
		if err != nil {
			notes = append(notes, api+": "+err.Error())
			continue
		}
		if ok, _ := m["success"].(bool); ok {
			d, _ := m["data"].(map[string]any)
			switch api {
			case "SYNO.FileStation.Info":
				if v, ok := d["is_manager"].(bool); ok {
					mgr = &v
				}
			case "SYNO.Core.Desktop.Initdata":
				if se, _ := d["Session"].(map[string]any); se != nil {
					user, _ = se["user"].(string)
					if v, ok := se["is_admin"].(bool); ok {
						mgr = &v
					}
				}
			case "SYNO.Core.NormalUser":
				if u, _ := d["username"].(string); u != "" {
					user = u
				}
			}
			if mgr == nil && api == "SYNO.Core.Desktop.Initdata" {
				// Rolle fehlt: File Station kennt is_manager (offiziell dokumentiert).
				valid = true
				continue
			}
			return true, true, mgr, user, ""
		}
		if valid {
			// Initdata war erfolgreich, nur die Rolle bleibt unbekannt.
			return true, true, mgr, user, ""
		}
		c := errCode(m)
		notes = append(notes, fmt.Sprintf("%s: %d", api, c))
		switch c {
		case 106, 107, 119:
			return false, true, nil, "", strings.Join(notes, ", ")
		case 105:
			return true, true, nil, "", strings.Join(notes, ", ")
		}
	}
	if valid {
		return true, true, mgr, user, ""
	}
	return false, false, nil, "", strings.Join(notes, ", ")
}

// IsAdmin reports admin rights for the admin UI.
func (a *Adapter) IsAdmin(s Session) bool {
	if a.AdminUsers[strings.ToLower(s.Account)] {
		return true
	}
	return s.IsManager != nil && *s.IsManager
}

func cookieSID(h http.Header) string {
	for _, line := range h.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err == nil && c.Name == "id" && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func remoteIP(r *http.Request) string { return upstream.ClientIP(r) }

// AppPrivKey: Schlüssel der Anwendung in ui/config. Unter diesem Namen führt
// DSM die Anwendungsberechtigung (SYNO.Core.Desktop.Initdata → AppPrivilege,
// vgl. Schema bei mikespub/synology; Drittanbieter-Einträge erscheinen dort
// mit ihrem ui/config-Schlüssel, z.B. com.synocommunity.packages.cops).
const AppPrivKey = "SYNO.SDS.DSVideoRevival"

// PrivRecheck: Abstand für die erneute Prüfung der Anwendungsberechtigung.
var PrivRecheck = 15 * time.Minute

type sessInfo struct {
	User    string
	IsAdmin *bool
	Priv    *bool // nil = nicht prüfbar
	Err     string
}

func (i sessInfo) note() string {
	switch {
	case i.Priv != nil:
		return ", Anwendungsberechtigung geprüft"
	case i.Err != "":
		return ", Anwendungsberechtigung nicht prüfbar: " + i.Err
	}
	return ""
}

// sessionInfo fragt DSM mit der Sitzung des Benutzers, wer er ist und ob er
// DS Video Revival nutzen darf. SYNO.Core.Desktop.Initdata ist nicht offiziell
// dokumentiert: fehlt die Angabe, wird nicht abgelehnt (nur protokolliert).
func (a *Adapter) sessionInfo(ctx context.Context, sid string) sessInfo {
	var out sessInfo
	if a.Upstream == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	m, err := a.Upstream.Call(ctx, "entry.cgi", url.Values{
		"api": {"SYNO.Core.Desktop.Initdata"}, "version": {"1"}, "method": {"get"}, "_sid": {sid},
	})
	if err == nil {
		// Direkt nach der Anmeldung ist AppPrivilege gelegentlich leer
		// (Feldtest 0.5.6) – einmal kurz warten und erneut fragen.
		if d, _ := m["data"].(map[string]any); d != nil {
			admin := false
			if se, _ := d["Session"].(map[string]any); se != nil {
				admin, _ = se["is_admin"].(bool)
			}
			// Administratoren werden ohnehin nie abgewiesen: nicht warten.
			if ap, _ := d["AppPrivilege"].(map[string]any); ap != nil && len(ap) == 0 && !admin {
				time.Sleep(700 * time.Millisecond)
				if m2, err2 := a.Upstream.Call(ctx, "entry.cgi", url.Values{
					"api": {"SYNO.Core.Desktop.Initdata"}, "version": {"1"}, "method": {"get"}, "_sid": {sid},
				}); err2 == nil {
					m = m2
				}
			}
		}
	}
	if err != nil {
		out.Err = err.Error()
	} else if ok, _ := m["success"].(bool); !ok {
		out.Err = fmt.Sprint(m["error"])
	} else {
		d, _ := m["data"].(map[string]any)
		if se, _ := d["Session"].(map[string]any); se != nil {
			out.User, _ = se["user"].(string)
			if v, ok := se["is_admin"].(bool); ok {
				out.IsAdmin = &v
			}
		}
		if ap, _ := d["AppPrivilege"].(map[string]any); ap != nil {
			if v, ok := ap[AppPrivKey].(bool); ok {
				out.Priv = &v
			} else if v, ok := ap["SYNO.ALLOW.ALL.APPLICATIONS"].(bool); ok && v {
				out.Priv = &v // Administratoren: alle Anwendungen erlaubt
			} else {
				// Schlüsselname für Drittanbieter-Apps nicht belegt: nach dem
				// Paketnamen suchen, sonst die vorhandenen Schlüssel protokollieren.
				var keys []string
				for k, x := range ap {
					keys = append(keys, k)
					if v, ok := x.(bool); ok && strings.Contains(strings.ToLower(k), "dsvideorevival") {
						out.Priv = &v
					}
				}
				if out.Priv == nil {
					sort.Strings(keys)
					out.Err = "Eintrag " + AppPrivKey + " fehlt (vorhanden: " + strings.Join(keys, ", ") + ")"
				}
			}
		} else {
			out.Err = "keine AppPrivilege-Angabe"
		}
	}
	if out.User == "" {
		// Fallback: SYNO.Core.NormalUser get (liefert username).
		if m, err := a.Upstream.Call(ctx, "entry.cgi", url.Values{
			"api": {"SYNO.Core.NormalUser"}, "version": {"1"}, "method": {"get"}, "_sid": {sid},
		}); err == nil {
			if d, _ := m["data"].(map[string]any); d != nil {
				out.User, _ = d["username"].(string)
			}
		}
	}
	return out
}

func (a *Adapter) logoutUpstream(sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = a.Upstream.Call(ctx, "auth.cgi", url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"logout"}, "_sid": {sid}})
}

// rewriteSession: session=from (z.B. VideoStation) → to (Formular und URL).
func rewriteSession(req *webapi.Request, raw []byte, from, to string) []byte {
	set := func(v url.Values) {
		if to == noSession {
			v.Del("session")
		} else {
			v.Set("session", to)
		}
	}
	if q := req.HTTP.URL.Query(); strings.EqualFold(q.Get("session"), from) {
		set(q)
		req.HTTP.URL.RawQuery = q.Encode()
	}
	if strings.HasPrefix(req.HTTP.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || req.HTTP.Header.Get("Content-Type") == "" {
		if v, err := url.ParseQuery(string(raw)); err == nil && strings.EqualFold(v.Get("session"), from) {
			set(v)
			raw = []byte(v.Encode())
			req.HTTP.ContentLength = int64(len(raw))
		}
	}
	return raw
}

// DSM behandelt „session“ als Anwendungsnamen und prüft dafür die
// Anwendungsberechtigung (normale Benutzer: 402 bei VideoStation, eigenen
// Namen und der Paket-ID, Feldtest 0.5.4). Ohne „session“ meldet DSM 7.1/7.2
// auch normale Benutzer an (Test in cmeans/mcp-synology #134) – das braucht
// dann keine Berechtigung für File Station oder DSM. Danach als Rückfall
// „webui“ und „FileStation“.
const noSession = "(ohne Sitzungsname)"

var sessionCandidates = []string{noSession, "webui", "FileStation"}

// prefFor: bewährter Sitzungsname des Kontos ("" = keiner bekannt).
func (a *Adapter) prefFor(account string) string {
	a.prefMu.Lock()
	defer a.prefMu.Unlock()
	a.loadPrefLocked()
	return a.prefSess[strings.ToLower(account)]
}

func (a *Adapter) loadPrefLocked() {
	if a.prefLoaded {
		return
	}
	a.prefLoaded = true
	if a.PrefFile == "" {
		return
	}
	if b, err := os.ReadFile(a.PrefFile); err == nil {
		m := map[string]string{}
		if json.Unmarshal(b, &m) == nil {
			if a.prefSess == nil {
				a.prefSess = map[string]string{}
			}
			for k, v := range m {
				if _, ok := a.prefSess[k]; !ok {
					a.prefSess[k] = v
				}
			}
		}
	}
}

func (a *Adapter) sessionOrder(account string) []string {
	p := a.prefFor(account)
	out := []string{}
	if p != "" {
		out = append(out, p)
	}
	for _, c := range sessionCandidates {
		if c != p {
			out = append(out, c)
		}
	}
	return out
}

func (a *Adapter) rememberSession(account, s string) {
	if account == "" || s == "" || s == "DSVideoRevival" || strings.EqualFold(s, "VideoStation") {
		return
	}
	a.prefMu.Lock()
	defer a.prefMu.Unlock()
	a.loadPrefLocked()
	if a.prefSess == nil {
		a.prefSess = map[string]string{}
	}
	if a.prefSess[strings.ToLower(account)] == s {
		return
	}
	a.prefSess[strings.ToLower(account)] = s
	if a.PrefFile != "" {
		if b, err := json.Marshal(a.prefSess); err == nil {
			_ = os.WriteFile(a.PrefFile, b, 0o600)
		}
	}
}

// isAdminInfo: Aussperrschutz – Administratoren werden nie wegen der
// Anwendungsberechtigung abgewiesen.
func (a *Adapter) isAdminInfo(account string, info sessInfo) bool {
	return (info.IsAdmin != nil && *info.IsAdmin) || a.AdminUsers[strings.ToLower(account)]
}

// noCookieWriter verwirft Set-Cookie der weitergereichten DSM-Antwort.
type noCookieWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *noCookieWriter) strip() {
	if !w.wrote {
		w.wrote = true
		w.ResponseWriter.Header().Del("Set-Cookie")
	}
}

func (w *noCookieWriter) WriteHeader(code int) {
	w.strip()
	w.ResponseWriter.WriteHeader(code)
}

func (w *noCookieWriter) Write(b []byte) (int, error) {
	w.strip()
	return w.ResponseWriter.Write(b)
}

package vs

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dsvideorevival/internal/auth"
)

func loginAs(t *testing.T, base, account, pw, ua string) string {
	t.Helper()
	form := url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"login"}, "account": {account}, "passwd": {pw}, "format": {"sid"}}
	req, _ := http.NewRequest("POST", base+"/webapi/entry.cgi", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	d, _ := m["data"].(map[string]any)
	sid, _ := d["sid"].(string)
	if sid == "" {
		t.Fatalf("Anmeldung %s: %v", account, m)
	}
	return sid
}

// movieCount: Anzahl Filme in der Liste der Sitzung (-1 = Fehler).
func movieCount(t *testing.T, base, sid, ua string) (int, int) {
	t.Helper()
	form := url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}}
	req, _ := http.NewRequest("POST", base+"/webapi/entry.cgi", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if m["success"] != true {
		return -1, errCode(m)
	}
	return len(m["data"].(map[string]any)["movie"].([]any)), 0
}

// TestSingleSession: „Nur eine Verbindung“ – die zweite Sitzung eines
// eingeschränkten Kontos sieht keine Medien, bis die erste ruht, sich
// abmeldet oder getrennt wird. Administratoren sind ausgenommen.
func TestSingleSession(t *testing.T) {
	ts, _, dir := newTestServer(t, "local")
	s := ts.Config.Handler.(*Server)
	h1, _ := auth.HashPassword("geheim")
	h2, _ := auth.HashPassword("gast-pw")
	b, _ := json.Marshal([]auth.LocalUser{{Account: "anna", Hash: h1, IsManager: true}, {Account: "gast", Hash: h2}})
	uf := filepath.Join(dir, "users.json")
	if err := os.WriteFile(uf, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s.Auth.Local, _ = auth.NewLocalUsers(uf)
	cfg := *s.Cfg()
	cfg.SingleSession = []string{"user:gast", "user:anna"}
	s.SetCfg(cfg)

	const tv = "TV DS video 1.1.8 rv:10085 (Dalvik/2.1.0 (Linux; U; Android 10; TV-X100 Build/QTG3.200305.006.S292))"
	const phone = "DS%20video/3.4.7 CFNetwork/1410.0.3 Darwin/22.6.0"
	tvSID := loginAs(t, ts.URL, "gast", "gast-pw", tv)
	phoneSID := loginAs(t, ts.URL, "gast", "gast-pw", phone)
	if n, _ := movieCount(t, ts.URL, tvSID, tv); n < 1 {
		t.Fatalf("erste Verbindung: %d Filme", n)
	}
	if n, _ := movieCount(t, ts.URL, phoneSID, phone); n != 0 {
		t.Fatalf("zweite Verbindung sieht %d Filme", n)
	}
	// Administratoren: ausgenommen
	a1, a2 := loginAs(t, ts.URL, "anna", "geheim", tv), loginAs(t, ts.URL, "anna", "geheim", phone)
	if n1, _ := movieCount(t, ts.URL, a1, tv); n1 < 1 {
		t.Fatal("Admin 1")
	}
	if n2, _ := movieCount(t, ts.URL, a2, phone); n2 < 1 {
		t.Fatal("Admin 2 gesperrt")
	}

	states := map[string]string{}
	for _, c := range s.Conns().List() {
		if c.Account == "gast" {
			states[c.Device] = c.State
		}
	}
	if states["Android TV · TV-X100"] != "active" || states["iOS"] != "blocked" {
		t.Fatalf("Liste: %v", states)
	}

	// Erste ruht länger als ConnIdle → zweite bekommt den Platz.
	s.Conns().mu.Lock()
	s.Conns().m[auth.HashSID(tvSID)].LastSeen = time.Now().Add(-ConnIdle - time.Second)
	s.Conns().mu.Unlock()
	if n, _ := movieCount(t, ts.URL, phoneSID, phone); n < 1 {
		t.Fatal("Platz nach Ruhezeit nicht frei")
	}
	if n, _ := movieCount(t, ts.URL, tvSID, tv); n != 0 {
		t.Fatal("ruhende Verbindung hat den Platz behalten")
	}

	// Abmelden gibt den Platz sofort frei.
	call(t, ts.URL, url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"logout"}, "_sid": {phoneSID}})
	if n, _ := movieCount(t, ts.URL, tvSID, tv); n < 1 {
		t.Fatal("Platz nach Abmelden nicht frei")
	}

	// Trennen: sid ungültig, Platz frei.
	third := loginAs(t, ts.URL, "gast", "gast-pw", phone)
	if n, _ := movieCount(t, ts.URL, third, phone); n != 0 {
		t.Fatal("dritte Verbindung nicht gesperrt")
	}
	key := auth.HashSID(tvSID)
	if _, _, ok := s.Conns().Kick(key, 0); !ok {
		t.Fatal("Kick")
	}
	s.Auth.Sessions.Revoke(key)
	if n, code := movieCount(t, ts.URL, tvSID, tv); n != -1 || code != 119 {
		t.Fatalf("getrennte Sitzung: %d Code %d", n, code)
	}
	if n, _ := movieCount(t, ts.URL, third, phone); n < 1 {
		t.Fatal("Platz nach Trennen nicht frei")
	}

	// Konto nicht mehr eingeschränkt: beide sehen alles.
	cfg = *s.Cfg()
	cfg.SingleSession = nil
	s.SetCfg(cfg)
	fourth := loginAs(t, ts.URL, "gast", "gast-pw", tv)
	if n, _ := movieCount(t, ts.URL, fourth, tv); n < 1 {
		t.Fatal("ohne Einschränkung gesperrt")
	}
}

// TestSingleSessionHLS: Wiedergabe über Job/Token zählt als Aktivität und
// wird für die zweite Verbindung gesperrt.
func TestSingleSessionStream(t *testing.T) {
	r := NewConns()
	if r.Touch("a", "gast", true, ConnInfo{Kind: "app"}) || !r.Touch("b", "gast", true, ConnInfo{Kind: "app"}) {
		t.Fatal("Platzvergabe")
	}
	r.Play("a", "job1", "Film A")
	r.Play("b", "job2", "Film B")
	if key, blocked := r.Stream("job1"); key != "a" || blocked {
		t.Fatal("job1")
	}
	if _, blocked := r.Stream("job2"); !blocked {
		t.Fatal("job2 nicht gesperrt")
	}
	gone := r.Begin("a", true)
	r.mu.Lock()
	r.m["a"].LastSeen = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	if !r.Blocked("b") {
		t.Fatal("laufende Wiedergabe hält den Platz nicht")
	}
	r.End("a", true)
	if _, jobs, ok := r.Kick("a", 0); !ok || len(jobs) != 1 || !gone.Load() {
		t.Fatalf("Kick: %v %v", jobs, gone.Load())
	}
	if r.Blocked("b") {
		t.Fatal("nach Kick gesperrt")
	}
}

func TestDeviceLabel(t *testing.T) {
	for ua, want := range map[string]string{
		"TV DS video 1.1.8 rv:10085 (Dalvik/2.1.0 (Linux; U; Android 10; TV-X100 Build/QTG3))":                                                    "Android TV · TV-X100",
		"Dalvik/2.1.0 (Linux; U; Android 13; Pixel 7 Build/TQ3A.230805.001)":                                                                      "Android · Pixel 7",
		"DS%20video/3.4.7 CFNetwork/1410.0.3 Darwin/22.6.0":                                                                                       "iOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36":                             "Browser · Chrome (Windows)",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1": "Browser · Safari (iPhone)",
	} {
		if got, _ := DeviceLabel(ua); got != want {
			t.Errorf("%q → %q, erwartet %q", ua, got, want)
		}
	}
}

// TestConnBanAndIdle: einstellbare Ruhezeit; Trennen mit Sperre hält das
// Gerät (Konto + Gerät + Adresse) fern, andere Geräte nicht.
func TestConnBanAndIdle(t *testing.T) {
	r := NewConns()
	idle := 2 * time.Minute
	r.Idle = func() time.Duration { return idle }
	tv := ConnInfo{Kind: "app", Device: "Android TV · TV-X100", Sure: true, IP: "10.0.0.5"}
	phone := ConnInfo{Kind: "app", Device: "iOS", Sure: true, IP: "10.0.0.7"}
	r.Touch("tv", "gast", true, tv)
	if !r.Touch("ph", "gast", true, phone) {
		t.Fatal("zweite Verbindung nicht gesperrt")
	}
	r.mu.Lock()
	r.m["tv"].LastSeen = time.Now().Add(-3 * time.Minute) // länger als die Ruhezeit (2 Min.)
	r.mu.Unlock()
	if r.Touch("ph", "gast", true, phone) {
		t.Fatal("Platz nach eingestellter Ruhezeit nicht frei")
	}
	// TV trennen und 30 Minuten sperren; neue Anmeldung desselben Geräts bleibt gesperrt
	if _, _, ok := r.Kick("ph", 30*time.Minute); !ok {
		t.Fatal("Kick")
	}
	if !r.Touch("ph2", "gast", true, phone) || r.BannedUntil("ph2").IsZero() || !r.Blocked("ph2") {
		t.Fatal("gesperrtes Gerät nach Neuanmeldung nicht gesperrt")
	}
	if r.Touch("tv2", "gast", true, tv) {
		t.Fatal("anderes Gerät gesperrt")
	}
	// auch ohne „Nur eine Verbindung“ gilt die Sperre
	if !r.Touch("ph3", "gast", false, phone) {
		t.Fatal("Sperre ohne Einschränkung nicht wirksam")
	}
	bans := r.Bans()
	if len(bans) != 1 || bans[0].Device != "iOS" || bans[0].IP != "10.0.0.7" {
		t.Fatalf("Sperren: %+v", bans)
	}
	if states := r.List(); len(states) == 0 {
		t.Fatal("Liste leer")
	}
	for _, v := range r.List() {
		if v.ID == "ph2" && (v.State != "banned" || v.Until.IsZero()) {
			t.Fatalf("Zustand: %+v", v)
		}
	}
	if _, ok := r.Unban(bans[0].ID); !ok {
		t.Fatal("Unban")
	}
	if r.Blocked("ph3") || r.Touch("ph3", "gast", false, phone) {
		t.Fatal("nach Aufheben weiter gesperrt")
	}
}

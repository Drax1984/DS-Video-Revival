package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/webapi"
)

// Anmeldung: Konto und Anwendungsberechtigung kommen aus
// SYNO.Core.Desktop.Initdata (auch bei verschlüsselter Anmeldung ohne
// lesbares „account“).
func TestLoginAppPrivilege(t *testing.T) {
	cases := []struct {
		name, initdata string
		wantOK         bool
		wantAccount    string
	}{
		{"erlaubt", `{"success":true,"data":{"Session":{"user":"lena","is_admin":false},"AppPrivilege":{"SYNO.SDS.DSVideoRevival":true}}}`, true, "lena"},
		{"verweigert", `{"success":true,"data":{"Session":{"user":"tom"},"AppPrivilege":{"SYNO.SDS.DSVideoRevival":false}}}`, false, ""},
		{"nicht prüfbar", `{"success":false,"error":{"code":105}}`, true, "?"},
		{"Admin nie ausgesperrt", `{"success":true,"data":{"Session":{"user":"admin","is_admin":true},"AppPrivilege":{"SYNO.SDS.DSVideoRevival":false}}}`, true, "admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loggedOut := false
			dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				q, _ := url.ParseQuery(string(b))
				w.Header().Set("Content-Type", "application/json")
				switch q.Get("api") + "/" + q.Get("method") {
				case "SYNO.API.Auth/login":
					io.WriteString(w, `{"data":{"sid":"S9"},"success":true}`)
				case "SYNO.API.Auth/logout":
					loggedOut = q.Get("_sid") == "S9"
					io.WriteString(w, `{"success":true}`)
				case "SYNO.Core.Desktop.Initdata/get":
					io.WriteString(w, tc.initdata)
				default:
					io.WriteString(w, `{"success":false,"error":{"code":103}}`)
				}
			}))
			defer dsm.Close()
			up, _ := upstream.New(dsm.URL)
			st := NewStore("")
			a := NewAdapter("dsm", up, nil, st, nil)
			body := "api=SYNO.API.Auth&version=6&method=login&__cIpHeRtExT=xyz&format=sid&session=VideoStation"
			r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			raw := []byte(body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			req, _ := webapi.Parse(r)
			rec := httptest.NewRecorder()
			a.HandleAuth(rec, req, raw)
			var m map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &m)
			ok, _ := m["success"].(bool)
			if ok != tc.wantOK {
				t.Fatalf("success=%v, Antwort %s", ok, rec.Body.String())
			}
			sess, have := st.Get("S9")
			if !tc.wantOK {
				if code := m["error"].(map[string]any)["code"].(float64); code != 402 || have || !loggedOut {
					t.Fatalf("Code %v, Sitzung %v, DSM-Abmeldung %v", code, have, loggedOut)
				}
				return
			}
			want := tc.wantAccount
			if want == "?" {
				want = ""
			}
			if !have || sess.Account != want {
				t.Fatalf("Sitzung %v Konto %q", have, sess.Account)
			}
		})
	}
}

// Anmeldung der Apps (session=VideoStation) geht als eigene Sitzung an DSM,
// sonst verweigert DSM normalen Benutzern mit 402 (Video-Station-Recht).
func TestLoginSessionRewrite(t *testing.T) {
	var got url.Values
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		w.Header().Set("Content-Type", "application/json")
		if q.Get("method") == "login" {
			got = q
		}
		io.WriteString(w, `{"data":{"sid":"S7"},"success":true}`)
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	st := NewStore("")
	a := NewAdapter("dsm", up, nil, st, nil)
	cipher := `{"rsa":"AbC+/=","aes":"x+y/z=="}`
	body := "api=SYNO.API.Auth&version=6&method=login&__cIpHeRtExT=" + url.QueryEscape(cipher) + "&session=VideoStation&format=sid"
	r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw := []byte(body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	req, _ := webapi.Parse(r)
	a.HandleAuth(httptest.NewRecorder(), req, raw)
	if got.Has("session") || got.Get("__cIpHeRtExT") != cipher {
		t.Fatalf("an DSM: %v", got)
	}
	if s, ok := st.Get("S7"); !ok || s.SessionName != "VideoStation" {
		t.Fatalf("Sitzung %v %+v", ok, s)
	}
}

// Verschlüsselte Anmeldung wie DS video (CgiEncryption): Paket-Schlüssel,
// Entschlüsselung, Anmeldung bei DSM mit session=DSVideoRevival.
func TestEncryptedLogin(t *testing.T) {
	var got url.Values
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		w.Header().Set("Content-Type", "application/json")
		if q.Get("method") == "login" {
			got = q
		}
		io.WriteString(w, `{"data":{"sid":"S8"},"success":true}`)
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	a := NewAdapter("dsm", up, nil, NewStore(""), nil)
	// getinfo
	r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader("api=SYNO.API.Encryption&version=1&method=getinfo"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req, _ := webapi.Parse(r)
	rec := httptest.NewRecorder()
	if !a.HandleEncryption(rec, req) {
		t.Fatal("getinfo nicht beantwortet")
	}
	var info struct {
		Data struct {
			Cipherkey, Ciphertoken string
			PublicKey              string `json:"public_key"`
			ServerTime             int64  `json:"server_time"`
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	der, _ := base64.StdEncoding.DecodeString(info.Data.PublicKey)
	pk, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	plain := info.Data.Ciphertoken + "=" + strconv.FormatInt(info.Data.ServerTime, 10) + "&account=Vicky&passwd=" + url.QueryEscape("p&ss=wört") + "&session=VideoStation"
	ct, _ := rsa.EncryptPKCS1v15(rand.Reader, pk.(*rsa.PublicKey), []byte(plain))
	body := "api=SYNO.API.Auth&version=6&method=login&" + info.Data.Cipherkey + "=" + url.QueryEscape(base64.StdEncoding.EncodeToString(ct)) + "&format=sid"
	r = httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw := []byte(body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	req, _ = webapi.Parse(r)
	a.HandleAuth(httptest.NewRecorder(), req, raw)
	if got.Get("account") != "Vicky" || got.Get("passwd") != "p&ss=wört" || got.Has("session") || got.Get(info.Data.Cipherkey) != "" || got.Get("format") != "sid" {
		t.Fatalf("an DSM: %v", got)
	}
}

// DSM verweigert „webui“ (402) → zweiter Versuch mit „FileStation“.
func TestLoginSessionFallback(t *testing.T) {
	var sessions []string
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		w.Header().Set("Content-Type", "application/json")
		if q.Get("method") == "login" {
			if !q.Has("session") {
				sessions = append(sessions, "-")
			} else {
				sessions = append(sessions, q.Get("session"))
			}
			if q.Get("session") != "FileStation" {
				io.WriteString(w, `{"error":{"code":402},"success":false}`)
				return
			}
			io.WriteString(w, `{"data":{"sid":"S5"},"success":true}`)
			return
		}
		io.WriteString(w, `{"success":false,"error":{"code":103}}`)
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	st := NewStore("")
	a := NewAdapter("dsm", up, nil, st, nil)
	body := "api=SYNO.API.Auth&version=6&method=login&account=kind&passwd=x&session=VideoStation&format=sid"
	r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw := []byte(body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	req, _ := webapi.Parse(r)
	rec := httptest.NewRecorder()
	a.HandleAuth(rec, req, raw)
	if strings.Join(sessions, ",") != "-,webui,FileStation" || !strings.Contains(rec.Body.String(), `"sid":"S5"`) {
		t.Fatalf("Versuche %v, Antwort %s", sessions, rec.Body.String())
	}
	// zweite Anmeldung: gelernter Sitzungsname zuerst
	sessions = nil
	r2 := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2.Body = io.NopCloser(bytes.NewReader(raw))
	req2, _ := webapi.Parse(r2)
	a.HandleAuth(httptest.NewRecorder(), req2, raw)
	if strings.Join(sessions, ",") != "FileStation" {
		t.Fatalf("zweite Anmeldung: %v", sessions)
	}
	if s, ok := st.Get("S5"); !ok || s.Account != "kind" {
		t.Fatalf("Sitzung %v %+v", ok, s)
	}
}

// Android (ohne format=sid): nach einer Ersatzanmeldung muss das Cookie „id“
// gesetzt sein, die App schickt die Sitzung nur per Cookie.
func TestFallbackLoginSetsCookie(t *testing.T) {
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		w.Header().Set("Content-Type", "application/json")
		if q.Get("method") == "login" && q.Get("session") != "FileStation" {
			http.SetCookie(w, &http.Cookie{Name: "id", Value: "FALSCH"})
			io.WriteString(w, `{"error":{"code":402},"success":false}`)
			return
		}
		io.WriteString(w, `{"data":{"sid":"S6"},"success":true}`)
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	a := NewAdapter("dsm", up, nil, NewStore(""), nil)
	body := "api=SYNO.API.Auth&version=6&method=login&account=kind&passwd=x&session=VideoStation&client_time=1"
	r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw := []byte(body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	req, _ := webapi.Parse(r)
	rec := httptest.NewRecorder()
	a.HandleAuth(rec, req, raw)
	ck := rec.Result().Cookies()
	if len(ck) != 1 || ck[0].Name != "id" || ck[0].Value != "S6" {
		t.Fatalf("Cookies %v, Antwort %s", ck, rec.Body.String())
	}
}

// TestWebIfLoginKeepsDSMCookie: Anmelden und Abmelden der Weboberfläche
// (session=DSVideoRevival) lassen das DSM-Cookie „id“ im Browser unberührt:
// keine Cookies an DSM, kein Set-Cookie zurück; abgemeldet wird nur die eigene sid.
func TestWebIfLoginKeepsDSMCookie(t *testing.T) {
	var cookies []string
	var logoutSID string
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		for k, v := range r.URL.Query() {
			q[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Get("api") == "SYNO.API.Auth" {
			cookies = append(cookies, r.Header.Get("Cookie"))
		}
		switch q.Get("method") {
		case "login":
			http.SetCookie(w, &http.Cookie{Name: "id", Value: "WEBIF", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "did", Value: "GERAET", Path: "/"})
			io.WriteString(w, `{"data":{"sid":"WEBIF"},"success":true}`)
		case "logout":
			logoutSID = q.Get("_sid")
			http.SetCookie(w, &http.Cookie{Name: "id", Value: "", Path: "/", MaxAge: -1})
			io.WriteString(w, `{"success":true}`)
		default:
			io.WriteString(w, `{"success":true,"data":{}}`)
		}
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	a := NewAdapter("dsm", up, nil, NewStore(""), nil)
	do := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Cookie", "id=DSM-DESKTOP; did=MEINGERAET")
		raw := []byte(body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		req, _ := webapi.Parse(r)
		rec := httptest.NewRecorder()
		a.HandleAuth(rec, req, raw)
		return rec
	}
	rec := do("api=SYNO.API.Auth&version=6&method=login&account=anna&passwd=x&session=DSVideoRevival&format=sid")
	if !strings.Contains(rec.Body.String(), "WEBIF") || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("Anmeldung: %s, Cookies %v", rec.Body.String(), rec.Result().Cookies())
	}
	rec = do("api=SYNO.API.Auth&version=6&method=logout&session=DSVideoRevival&_sid=WEBIF")
	if logoutSID != "WEBIF" || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("Abmeldung: sid %q, Cookies %v", logoutSID, rec.Result().Cookies())
	}
	for _, c := range cookies {
		if c != "" {
			t.Fatalf("Browser-Cookies an DSM weitergereicht: %q", c)
		}
	}
	// Apps (andere Sitzungsnamen) behalten DSMs Cookie wie bisher.
	rec = do("api=SYNO.API.Auth&version=6&method=login&account=anna&passwd=x&session=FileStation")
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("App-Anmeldung ohne Cookie")
	}
}

// Weboberfläche (session=DSVideoRevival): Lehnt DSM den Namen ab, wird der
// bewährte Sitzungsname gemerkt – auch über einen Neustart – und beim nächsten
// Mal sofort genommen (eine DSM-Anmeldung statt zwei).
func TestWebIfLoginRemembersSession(t *testing.T) {
	var sessions []string
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		w.Header().Set("Content-Type", "application/json")
		if q.Get("method") == "login" {
			s := q.Get("session")
			if !q.Has("session") {
				s = "-"
			}
			sessions = append(sessions, s)
			if s == "DSVideoRevival" {
				io.WriteString(w, `{"error":{"code":402},"success":false}`)
				return
			}
			io.WriteString(w, `{"data":{"sid":"W1"},"success":true}`)
			return
		}
		io.WriteString(w, `{"success":false,"error":{"code":103}}`)
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	pref := filepath.Join(t.TempDir(), "auth-sessions.json")
	login := func(a *Adapter) {
		body := "api=SYNO.API.Auth&version=6&method=login&account=lena&passwd=x&session=DSVideoRevival&format=sid"
		r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req, _ := webapi.Parse(r)
		rec := httptest.NewRecorder()
		a.HandleAuth(rec, req, []byte(body))
		if !strings.Contains(rec.Body.String(), `"sid":"W1"`) {
			t.Fatalf("Antwort %s", rec.Body.String())
		}
	}
	a := NewAdapter("dsm", up, nil, NewStore(""), nil)
	a.PrefFile = pref
	login(a)
	if strings.Join(sessions, ",") != "DSVideoRevival,-" {
		t.Fatalf("erste Anmeldung: %v", sessions)
	}
	// Neustart: neuer Adapter, gemerkter Name aus der Datei
	sessions = nil
	b := NewAdapter("dsm", up, nil, NewStore(""), nil)
	b.PrefFile = pref
	login(b)
	if strings.Join(sessions, ",") != "-" {
		t.Fatalf("nach Neustart: %v", sessions)
	}
}

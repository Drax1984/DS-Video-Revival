package admin

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/vs"
)

// TestAccessSingleSession: „Nur eine Verbindung“ speichern und bereinigen.
func TestAccessSingleSession(t *testing.T) {
	s := &vs.Server{}
	c := config.Defaults()
	c.Groups = map[string][]string{"familie": {"tom"}}
	s.SetCfg(c)
	h := &Handler{S: s, CfgPath: filepath.Join(t.TempDir(), "config.json")}
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.access(rec, httptest.NewRequest("PUT", "/dsvr/api/access", strings.NewReader(body)), "")
		if rec.Code != 200 {
			t.Fatalf("PUT %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	put(`{"groups":{"familie":["tom"]},"single_session":["vgroup:familie","user:Gast","vgroup:administrators","x"]}`)
	if got := s.Cfg().SingleSession; !reflect.DeepEqual(got, []string{"vgroup:familie", "user:gast"}) {
		t.Fatalf("gespeichert: %v", got)
	}
	put(`{"groups":{}}`) // Gruppe gelöscht, Feld fehlt → unverändert bis auf die Gruppe
	if got := s.Cfg().SingleSession; !reflect.DeepEqual(got, []string{"user:gast"}) {
		t.Fatalf("nach Gruppe löschen: %v", got)
	}
	rec := httptest.NewRecorder()
	h.access(rec, httptest.NewRequest("GET", "/dsvr/api/access", nil), "")
	var v struct {
		SingleSession []string `json:"single_session"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !reflect.DeepEqual(v.SingleSession, []string{"user:gast"}) {
		t.Fatalf("GET: %s", rec.Body)
	}
}

// TestConnectionsKick: Liste der Verbindungen und Trennen über die Weboberfläche.
func TestConnectionsKick(t *testing.T) {
	store := auth.NewStore("")
	s := &vs.Server{Auth: auth.NewAdapter("local", nil, nil, store, nil)}
	s.SetCfg(config.Defaults())
	h := &Handler{S: s, raw: map[string]rawToken{}}
	store.Put("ADMIN", auth.Session{Account: "anna"})
	store.Put("TV", auth.Session{Account: "gast"})
	admin, _ := store.Get("ADMIN")
	tv, _ := store.Get("TV")
	req := httptest.NewRequest("GET", "/dsvr/api/connections", nil)
	req.Header.Set("User-Agent", "TV DS video 1.1.8 rv:10085 (Dalvik/2.1.0 (Linux; U; Android 10; TV-X100 Build/X))")
	s.TouchConn(tv, req, "app")
	s.TouchConn(admin, httptest.NewRequest("GET", "/dsvr/api/me", nil), "web")
	h.raw["tok"] = rawToken{path: "/x", account: "gast", key: tv.Key}

	rec := httptest.NewRecorder()
	h.connections(rec, httptest.NewRequest("GET", "/dsvr/api/connections", nil), admin)
	var l struct {
		Connections []vs.ConnView `json:"connections"`
		Self        string        `json:"self"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || len(l.Connections) != 2 || l.Self != admin.Key {
		t.Fatalf("Liste: %s", rec.Body)
	}
	// eigene Sitzung nicht trennbar
	rec = httptest.NewRecorder()
	h.connections(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"`+admin.Key+`"}`)), admin)
	if rec.Code != 400 {
		t.Fatalf("eigene Sitzung: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.connections(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"`+tv.Key+`"}`)), admin)
	if rec.Code != 200 {
		t.Fatalf("Trennen: %d %s", rec.Code, rec.Body)
	}
	if _, code := s.Auth.Validate(req.Context(), "TV"); code != 119 {
		t.Fatalf("getrennte Sitzung gültig (Code %d)", code)
	}
	store.Put("TV", auth.Session{Account: "gast"}) // Wiederbelebung verhindert
	if _, ok := store.Get("TV"); ok {
		t.Fatal("getrennte Sitzung wiederbelebt")
	}
	if _, ok := h.raw["tok"]; ok {
		t.Fatal("Wiedergabe-Token nicht entfernt")
	}
	if n := len(s.Conns().List()); n != 1 {
		t.Fatalf("nach Trennen %d Verbindungen", n)
	}
}

// TestConnectionsBlock: Trennen mit Sperre, Sperrliste, Aufheben; Ruhezeit speichern.
func TestConnectionsBlock(t *testing.T) {
	store := auth.NewStore("")
	s := &vs.Server{Auth: auth.NewAdapter("local", nil, nil, store, nil)}
	s.SetCfg(config.Defaults())
	h := &Handler{S: s, raw: map[string]rawToken{}, CfgPath: filepath.Join(t.TempDir(), "config.json")}
	store.Put("ADMIN", auth.Session{Account: "anna"})
	store.Put("TV", auth.Session{Account: "gast"})
	admin, _ := store.Get("ADMIN")
	tv, _ := store.Get("TV")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", "TV DS video 1.1.8 rv:10085 (Dalvik/2.1.0 (Linux; U; Android 10; TV-X100 Build/X))")
	s.TouchConn(tv, req, "app")
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.connections(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)), admin)
		return rec.Code
	}
	if code := post(`{"id":"` + tv.Key + `","block_minutes":99999}`); code != 400 {
		t.Fatalf("zu lange Sperre: %d", code)
	}
	if code := post(`{"id":"` + tv.Key + `","block_minutes":30}`); code != 200 {
		t.Fatalf("Trennen mit Sperre: %d", code)
	}
	// App meldet sich neu an → gleiche Gerätekennung → gesperrt
	store.Put("TV2", auth.Session{Account: "gast"})
	tv2, _ := store.Get("TV2")
	if !s.TouchConn(tv2, req, "app") || !s.ConnBlocked(tv2) {
		t.Fatal("Neuanmeldung nicht gesperrt")
	}
	rec := httptest.NewRecorder()
	h.connections(rec, httptest.NewRequest("GET", "/", nil), admin)
	var l struct {
		Bans []vs.Ban `json:"bans"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if len(l.Bans) != 1 || l.Bans[0].Account != "gast" {
		t.Fatalf("Sperrliste: %s", rec.Body)
	}
	if code := post(`{"unban":"` + l.Bans[0].ID + `"}`); code != 200 || s.ConnBlocked(tv2) {
		t.Fatalf("Aufheben: %d", code)
	}
	// Ruhezeit
	put := func(body string) int {
		rec := httptest.NewRecorder()
		h.access(rec, httptest.NewRequest("PUT", "/dsvr/api/access", strings.NewReader(body)), "")
		return rec.Code
	}
	if code := put(`{"single_session_idle":0}`); code != 400 {
		t.Fatalf("Ruhezeit 0: %d", code)
	}
	if code := put(`{"single_session_idle":15}`); code != 200 || s.ConnIdle() != 15*time.Minute {
		t.Fatalf("Ruhezeit 15: %d %v", code, s.ConnIdle())
	}
	if code := put(`{"single_session_idle":5}`); code != 200 || s.Cfg().SingleSessionIdle != 0 || s.ConnIdle() != 5*time.Minute {
		t.Fatalf("Ruhezeit Standard: %d %d", code, s.Cfg().SingleSessionIdle)
	}
}

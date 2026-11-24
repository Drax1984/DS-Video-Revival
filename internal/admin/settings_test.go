package admin

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dsvideorevival/internal/config"
	"dsvideorevival/internal/vs"
)

// TestSettingsPorts: Port der Weboberfläche und der Jellyfin-Schnittstelle
// prüfen (reserviert, doppelt, belegt) und Neustart-Hinweis.
func TestSettingsPorts(t *testing.T) {
	s := &vs.Server{}
	c := config.Defaults()
	s.SetCfg(c)
	h := &Handler{S: s, CfgPath: filepath.Join(t.TempDir(), "config.json")}
	put := func(body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.settings(rec, httptest.NewRequest("PUT", "/dsvr/api/settings", strings.NewReader(body)))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	if code, m := put(`{"web_port":5000}`); code != 400 || !strings.Contains(m["error"].(string), "reserviert") {
		t.Fatalf("5000: %d %v", code, m)
	}
	if code, m := put(`{"web_port":8096,"jellyfin_enabled":true,"jellyfin_port":8096}`); code != 400 {
		t.Fatalf("doppelt: %d %v", code, m)
	}
	busy, _ := net.Listen("tcp", ":0")
	defer busy.Close()
	bp := busy.Addr().(*net.TCPAddr).Port
	if code, m := put(`{"web_port":` + strconv.Itoa(bp) + `}`); code != 400 || !strings.Contains(m["error"].(string), "belegt") {
		t.Fatalf("belegt: %d %v", code, m)
	}
	free, _ := net.Listen("tcp", ":0")
	fp := free.Addr().(*net.TCPAddr).Port
	free.Close()
	code, m := put(`{"web_port":` + strconv.Itoa(fp) + `}`)
	if code != 200 || m["restart"] != true || s.Cfg().ListenHTTP != ":"+strconv.Itoa(fp) {
		t.Fatalf("neuer Port: %d %v %s", code, m, s.Cfg().ListenHTTP)
	}
	// Jellyfin einschalten wirkt ohne Neustart
	code, m = put(`{"jellyfin_enabled":true,"jellyfin_port":0}`)
	if code != 200 || m["restart"] != true /* Webport weiterhin ausstehend */ || !s.Cfg().JellyfinEnabled || s.Cfg().JellyfinListenPort() != 8096 {
		t.Fatalf("Jellyfin: %d %v", code, m)
	}
	rec := httptest.NewRecorder()
	h.settings(rec, httptest.NewRequest("GET", "/dsvr/api/settings", nil))
	var v settingsView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.WebPort != fp || v.RunningWebPort != 5180 || !v.JellyfinEnabled || v.JellyfinPort != 8096 {
		t.Fatalf("GET: %+v", v)
	}
}

// TestUpdateMenuPort: Ausweichport im DSM-Menüeintrag (ui/open.js) folgt dem
// Port der Weboberfläche.
func TestUpdateMenuPort(t *testing.T) {
	dest := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dest, "ui"), 0o755)
	src, err := os.ReadFile("../../packaging/synology/ui/open.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dest, "ui", "open.js"), src, 0o644)
	t.Setenv("SYNOPKG_PKGDEST", dest)
	updateMenuPort(":5280")
	b, _ := os.ReadFile(filepath.Join(dest, "ui", "open.js"))
	if !strings.Contains(string(b), `var port = "5280";`) || strings.Count(string(b), "5180") != 0 {
		t.Fatalf("open.js nicht angepasst:\n%s", b)
	}
}

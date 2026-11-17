package admin

import (
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dsvideorevival/internal/config"
	"dsvideorevival/internal/vs"
)

// TestAccessDownloadAllow: Herunterladen-Recht speichern, bereinigen und beim
// Löschen einer Gruppe nachziehen.
func TestAccessDownloadAllow(t *testing.T) {
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
	put(`{"groups":{"familie":["tom"]},"download_allow":["vgroup:familie","user:Lena","unsinn","vgroup:administrators","user:lena"]}`)
	if got := s.Cfg().DownloadAllow; !reflect.DeepEqual(got, []string{"vgroup:familie", "user:lena"}) {
		t.Fatalf("nach Speichern: %v", got)
	}
	put(`{"groups":{}}`) // Gruppe gelöscht, ältere Oberfläche ohne download_allow
	if got := s.Cfg().DownloadAllow; !reflect.DeepEqual(got, []string{"user:lena"}) {
		t.Fatalf("nach Gruppe löschen: %v", got)
	}
	put(`{"groups":{},"download_allow":[]}`)
	if got := s.Cfg().DownloadAllow; len(got) != 0 {
		t.Fatalf("Alle: %v", got)
	}
	cfg, err := config.Load(h.CfgPath)
	if err != nil || len(cfg.DownloadAllow) != 0 {
		t.Fatalf("gespeichert: %v %v", cfg.DownloadAllow, err)
	}
}

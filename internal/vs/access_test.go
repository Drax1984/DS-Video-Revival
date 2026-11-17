package vs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/webapi"
)

func TestAccessFor(t *testing.T) {
	gf := filepath.Join(t.TempDir(), "group")
	_ = os.WriteFile(gf, []byte("administrators:x:101:admin,papa\nusers:x:100:\nkinder:x:65536:lena,tom\nfamilie:x:65537:papa,lena,tom\n"), 0o644)
	old := GroupFile
	GroupFile = gf
	grpCache.mem = nil
	defer func() { GroupFile = old; grpCache.mem = nil }()

	s := &Server{}
	s.SetCfg(config.Config{
		Folders: []config.Folder{{Path: "/v/Filme"}, {Path: "/v/Heim", Allow: []string{"group:familie"}}, {Path: "/v/Erw", Allow: []string{"user:papa"}}},
		Restrictions: map[string]config.Restriction{
			"group:kinder": {MaxFSK: 12, BlockUnrated: true},
			"user:tom":     {MaxFSK: 16},
		},
	})
	a := s.AccessFor(auth.Session{Account: "papa"})
	if len(a.Folders) != 3 || a.MaxFSK != -1 || a.BlockUnrated {
		t.Fatalf("papa: %+v", a)
	}
	a = s.AccessFor(auth.Session{Account: "Lena"})
	if len(a.Folders) != 2 || a.MaxFSK != 12 || !a.BlockUnrated {
		t.Fatalf("lena: %+v", a)
	}
	a = s.AccessFor(auth.Session{Account: "tom"}) // eigene Regel vor Gruppe
	if a.MaxFSK != 16 || a.BlockUnrated {
		t.Fatalf("tom: %+v", a)
	}
	a = s.AccessFor(auth.Session{Account: "gast"})
	if len(a.Folders) != 1 || a.MaxFSK != -1 {
		t.Fatalf("gast: %+v", a)
	}
	a = s.AccessFor(auth.Session{Account: ""}) // unbekannt → strengste Regel
	if len(a.Folders) != 1 || a.MaxFSK != 12 || !a.BlockUnrated {
		t.Fatalf("unbekannt: %+v", a)
	}
	// eigene Gruppen
	c := *s.Cfg()
	c.Groups = map[string][]string{"kleine": {"max"}}
	c.Restrictions = map[string]config.Restriction{"vgroup:kleine": {MaxFSK: 6, BlockUnrated: true}}
	c.Folders = append(c.Folders, config.Folder{Path: "/v/Kinder", Allow: []string{"vgroup:kleine"}})
	s.SetCfg(c)
	a = s.AccessFor(auth.Session{Account: "Max"})
	if a.MaxFSK != 6 || len(a.Folders) != 2 {
		t.Fatalf("max: %+v", a)
	}
	// Aussperrschutz: Admins ohne Einschränkung
	s.Auth = auth.NewAdapter("local", nil, nil, auth.NewStore(""), []string{"max"})
	if s.AccessFor(auth.Session{Account: "max"}) != nil {
		t.Fatal("Admin eingeschränkt")
	}
	s.Auth = nil
	s.SetCfg(config.Config{Folders: []config.Folder{{Path: "/v/Filme"}}})
	if s.AccessFor(auth.Session{Account: "x"}) != nil {
		t.Fatal("ohne Regeln muss alles frei sein")
	}
}

// Mitglieder der eigenen Gruppe „administrators“ sind Admins in DS Video
// Revival (sehen alles, is_manager).
func TestAppAdminGroup(t *testing.T) {
	gf := filepath.Join(t.TempDir(), "group")
	_ = os.WriteFile(gf, []byte("administrators:x:101:admin\nkinder:x:65536:lena,tom\n"), 0o644)
	old := GroupFile
	GroupFile = gf
	grpCache.mem = nil
	defer func() { GroupFile = old; grpCache.mem = nil }()

	s := &Server{}
	s.SetCfg(config.Config{
		Folders:      []config.Folder{{Path: "/v/Erw", Allow: []string{"user:papa"}}},
		Restrictions: map[string]config.Restriction{"group:kinder": {MaxFSK: 12}},
		Groups:       map[string][]string{config.AdminGroup: {"Lena"}},
	})
	if !s.IsAppAdmin(auth.Session{Account: "lena"}) || s.IsAppAdmin(auth.Session{Account: "tom"}) || s.IsAppAdmin(auth.Session{Account: ""}) {
		t.Fatal("IsAppAdmin")
	}
	if a := s.AccessFor(auth.Session{Account: "lena"}); a != nil {
		t.Fatalf("lena als App-Admin eingeschränkt: %+v", a)
	}
	if a := s.AccessFor(auth.Session{Account: "tom"}); a == nil || a.MaxFSK != 12 {
		t.Fatalf("tom: %+v", a)
	}
}

func TestMayDownload(t *testing.T) {
	gf := filepath.Join(t.TempDir(), "group")
	_ = os.WriteFile(gf, []byte("kinder:x:65536:lena,tom\n"), 0o644)
	old := GroupFile
	GroupFile = gf
	grpCache.mem = nil
	defer func() { GroupFile = old; grpCache.mem = nil }()

	s := &Server{}
	s.SetCfg(config.Config{})
	if !s.MayDownload(auth.Session{Account: "tom"}) {
		t.Fatal("ohne Einschränkung muss jeder dürfen")
	}
	s.SetCfg(config.Config{
		DownloadAllow: []string{"vgroup:familie", "group:Kinder"},
		Groups:        map[string][]string{"familie": {"Papa"}, config.AdminGroup: {"oma"}},
	})
	for acc, want := range map[string]bool{"papa": true, "tom": true, "lena": true, "gast": false, "oma": true, "": false} {
		if got := s.MayDownload(auth.Session{Account: acc}); got != want {
			t.Errorf("%q: %v, erwartet %v", acc, got, want)
		}
	}
}

// TestDownloadDenied: ohne Recht weder Download (HTTP 403) noch Offline-Umwandlung.
func TestDownloadDenied(t *testing.T) {
	s := &Server{}
	s.SetCfg(config.Config{DownloadAllow: []string{"user:papa"}})
	run := func(fn handlerFunc, query string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest("GET", "/webapi/entry.cgi?"+query, nil)
		req, err := webapi.Parse(r)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		fn(&Ctx{W: rec, Req: req, Sess: auth.Session{Account: "gast"}, S: s})
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec, m
	}
	rec, m := run(fileDownload, "api=SYNO.VideoStation2.File&version=2&method=download&id=1")
	if rec.Code != http.StatusForbidden || m["success"] != false {
		t.Fatalf("Download: %d %v", rec.Code, m)
	}
	_, m = run(offlineCreate, "api=SYNO.VideoStation2.OfflineConversion&version=1&method=create&file_id=1&video_profile=high")
	if m["success"] != false || m["error"].(map[string]any)["code"] != float64(webapi.ErrPermission) {
		t.Fatalf("create: %v", m)
	}
	if offlineAvailable(&Ctx{S: s, Sess: auth.Session{Account: "gast"}}) {
		t.Fatal("Privileg offline_conversion trotz fehlendem Download-Recht")
	}
}

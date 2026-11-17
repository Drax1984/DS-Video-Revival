package admin

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/vs"
)

// TestTreeAndPaths: Ordneransicht und Dateipfade nur für DSM-Administratoren.
func TestTreeAndPaths(t *testing.T) {
	root := t.TempDir()
	filme, serien := filepath.Join(root, "Filme"), filepath.Join(root, "Serien")
	for _, p := range []string{
		filepath.Join(filme, "Film A (2020)", "Film A (2020).mkv"),
		filepath.Join(filme, "Film B (2001).mkv"),
		filepath.Join(serien, "Kanalwelt", "Staffel 1", "Kanalwelt - S01E01.mkv"),
		filepath.Join(serien, "Kanalwelt", "Staffel 2", "Kanalwelt - S02E01.mkv"),
	} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	cfg := config.Defaults()
	cfg.Folders = []config.Folder{{Path: filme, Type: "movie"}, {Path: serien, Type: "tvshow"}}
	lib, _ := library.Open(filepath.Join(t.TempDir(), "lib.json"))
	if _, err := lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	s := &vs.Server{Lib: lib, Auth: &auth.Adapter{}}
	s.SetCfg(cfg)
	h := &Handler{S: s}
	yes, no := true, false
	admin, gast := auth.Session{Account: "admin", IsManager: &yes}, auth.Session{Account: "gast", IsManager: &no}

	get := func(fn func(*httptest.ResponseRecorder), v any) int {
		rec := httptest.NewRecorder()
		fn(rec)
		_ = json.Unmarshal(rec.Body.Bytes(), v)
		return rec.Code
	}
	var tr []treeRoot
	if code := get(func(w *httptest.ResponseRecorder) {
		h.tree(w, httptest.NewRequest("GET", "/dsvr/api/tree", nil), admin)
	}, &tr); code != 200 || len(tr) != 2 {
		t.Fatalf("tree: %d %+v", code, tr)
	}
	var rels []string
	for _, d := range tr[0].Dirs {
		rels = append(rels, d.Rel)
	}
	if !reflect.DeepEqual(rels, []string{"", "Film A (2020)"}) || tr[0].Library != "Filme" || tr[0].Count != 2 {
		t.Fatalf("Filme: %v %+v", rels, tr[0])
	}
	ep := tr[1].Dirs[0].Files[0]
	if tr[1].Dirs[0].Rel != filepath.Join("Kanalwelt", "Staffel 1") || ep.Title != "Kanalwelt" || ep.Kind != "episode" || ep.ShowID == 0 || ep.Sub[:6] != "S01E01" {
		t.Fatalf("Serien: %+v", tr[1].Dirs)
	}
	var e map[string]any
	if code := get(func(w *httptest.ResponseRecorder) { h.tree(w, httptest.NewRequest("GET", "/dsvr/api/tree", nil), gast) }, &e); code != 403 {
		t.Fatalf("tree ohne DSM-Admin: %d", code)
	}

	var movies, shows []libEntry
	get(func(w *httptest.ResponseRecorder) {
		h.library(w, httptest.NewRequest("GET", "/dsvr/api/library?kind=movie", nil), admin)
	}, &movies)
	if len(movies) != 2 || len(movies[0].Paths) != 1 || filepath.Base(movies[0].Paths[0]) != "Film A (2020).mkv" {
		t.Fatalf("Pfade Filme: %+v", movies)
	}
	get(func(w *httptest.ResponseRecorder) {
		h.library(w, httptest.NewRequest("GET", "/dsvr/api/library?kind=tvshow", nil), admin)
	}, &shows)
	if len(shows) != 1 || !reflect.DeepEqual(shows[0].Paths, []string{filepath.Join(serien, "Kanalwelt")}) {
		t.Fatalf("Pfade Serien: %+v", shows)
	}
	movies = nil
	get(func(w *httptest.ResponseRecorder) {
		h.library(w, httptest.NewRequest("GET", "/dsvr/api/library?kind=movie", nil), gast)
	}, &movies)
	if len(movies) != 2 || movies[0].Paths != nil {
		t.Fatalf("Pfade für Nicht-Admin sichtbar: %+v", movies)
	}
}

func TestShowDirs(t *testing.T) {
	for _, c := range []struct {
		in, want []string
	}{
		{[]string{"/volume1/video/Serien/X - S01E01.mkv", "/volume1/video/Serien/X - S01E02.mkv"}, []string{"/volume1/video/Serien"}},
		{[]string{"/volume1/video/Serien/X/S1/a.mkv", "/volume1/video/Serien/X/S2/b.mkv"}, []string{"/volume1/video/Serien/X"}},
		{[]string{"/volume1/a/X/a.mkv", "/volume2/b/X/b.mkv"}, []string{"/volume1/a/X", "/volume2/b/X"}},
		{nil, nil},
	} {
		if got := showDirs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v → %v, erwartet %v", c.in, got, c.want)
		}
	}
}

// TestTreeDupsAndDeleteFile: Doppelgänger erkennen, einzelne Datei löschen.
func TestTreeDupsAndDeleteFile(t *testing.T) {
	root := t.TempDir()
	filme := filepath.Join(root, "Filme")
	a1 := filepath.Join(filme, "Film A (2020)", "Film A (2020).mkv")
	a2 := filepath.Join(filme, "Kopie", "Film A (2020).mkv")
	b := filepath.Join(filme, "Film B (2001).mkv")
	for _, p := range []string{a1, a2, b} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	cfg := config.Defaults()
	cfg.Folders = []config.Folder{{Path: filme, Type: "movie"}}
	lib, _ := library.Open(filepath.Join(t.TempDir(), "lib.json"))
	if _, err := lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	s := &vs.Server{Lib: lib, Auth: &auth.Adapter{}}
	s.SetCfg(cfg)
	h := &Handler{S: s}
	yes, no := true, false
	admin, gast := auth.Session{Account: "admin", IsManager: &yes}, auth.Session{Account: "gast", IsManager: &no}

	files := func() map[string]treeFile {
		rec := httptest.NewRecorder()
		h.tree(rec, httptest.NewRequest("GET", "/dsvr/api/tree", nil), admin)
		var tr []treeRoot
		_ = json.Unmarshal(rec.Body.Bytes(), &tr)
		m := map[string]treeFile{}
		for _, d := range tr[0].Dirs {
			for _, f := range d.Files {
				m[f.Path] = f
			}
		}
		return m
	}
	m := files()
	if m[a1].Dups != 2 || m[a2].Dups != 2 || m[a1].Dup != m[a2].Dup || m[b].Dups != 1 || m[a1].FileID == 0 {
		t.Fatalf("Doppelgänger: %+v", m)
	}
	del := func(sess auth.Session, id int) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.deleteFile(rec, httptest.NewRequest("POST", "/dsvr/api/delete-file", strings.NewReader(`{"file_id":`+strconv.Itoa(id)+`}`)), sess, "")
		var r map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return rec.Code, r
	}
	if code, _ := del(gast, m[a2].FileID); code != 403 {
		t.Fatalf("Nicht-Admin: %d", code)
	}
	if code, r := del(admin, m[a2].FileID); code != 200 || r["ok"] != true {
		t.Fatalf("Löschen: %d %v", code, r)
	}
	if _, err := os.Stat(a2); !os.IsNotExist(err) {
		t.Fatalf("Datei noch da: %v", err)
	}
	if _, err := os.Stat(a1); err != nil {
		t.Fatalf("falsche Datei gelöscht: %v", err)
	}
	m = files()
	if _, ok := m[a2]; ok || m[a1].Dups != 1 {
		t.Fatalf("nach Löschen: %+v", m)
	}
}

func TestPartKey(t *testing.T) {
	for p, want := range map[string]string{
		"/v/Film (1995)/Film.1995.CD1.avi":                                  ":teil1",
		"/v/Film (1995)/Film.1995.cd2.avi":                                  ":teil2",
		"/v/Film/film-part2.mkv":                                            ":teil2",
		"/v/Film/Film - Teil 02.mkv":                                        ":teil2",
		"/v/Film/film.disc.b.mkv":                                           ":teil2",
		"/v/Film/CD2/film.avi":                                              ":teil2",
		"/v/Harry Potter Part 2/Harry.Potter.Part.2.mkv":                    ":teil2",
		"/v/Film/Film.2020.1080p.mkv":                                       "",
		"/v/Film/Sample/film.sample.mkv":                                    "",
		"/v/Captain/Captain.America.1990.German.DVDRip.avi":                 "",
		"/v/Apt/Apartment.mkv":                                              "",
		"/v/Filme/altfilm/1von3.mpg":                                        ":teil1",
		"/v/Filme/altfilm/3von3.mpg":                                        ":teil3",
		"/v/Film/Film 2 von 3.avi":                                          ":teil2",
		"/v/Film/film.1of2.avi":                                             ":teil1",
		"/v/Serien/Zeichentrick/SERIE_SEASON1_DISC_3/VIDEO_TS/VTS_01_2.VOB": ":dvd:serie_season1_disc_3/vts_01_2.vob",
		"/v/Film/VIDEO_TS/VIDEO_TS.IFO":                                     ":dvd:film/video_ts.ifo",
		"/v/Film/film.vob":                                                  ":dvd:film/film.vob",
	} {
		if got := partKey(p); got != want {
			t.Errorf("%s → %q, erwartet %q", p, got, want)
		}
	}
}

// Zwei Teile (CD1/CD2) eines Films sind keine Doppelgänger, zwei Kopien schon.
func TestTreeDupsParts(t *testing.T) {
	root := t.TempDir()
	filme := filepath.Join(root, "Filme")
	for _, p := range []string{
		filepath.Join(filme, "Alter Film (1995)", "Alter Film (1995) CD1.avi"),
		filepath.Join(filme, "Alter Film (1995)", "Alter Film (1995) CD2.avi"),
		filepath.Join(filme, "Kopie", "Alter Film (1995) CD1.avi"),
	} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	cfg := config.Defaults()
	cfg.Folders = []config.Folder{{Path: filme, Type: "movie"}}
	lib, _ := library.Open(filepath.Join(t.TempDir(), "lib.json"))
	if _, err := lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	s := &vs.Server{Lib: lib, Auth: &auth.Adapter{}}
	s.SetCfg(cfg)
	h := &Handler{S: s}
	yes := true
	rec := httptest.NewRecorder()
	h.tree(rec, httptest.NewRequest("GET", "/dsvr/api/tree", nil), auth.Session{Account: "admin", IsManager: &yes})
	var tr []treeRoot
	_ = json.Unmarshal(rec.Body.Bytes(), &tr)
	got := map[string]int{}
	for _, d := range tr[0].Dirs {
		for _, f := range d.Files {
			got[filepath.Base(filepath.Dir(f.Path))+"/"+f.Name] = f.Dups
		}
	}
	want := map[string]int{"Alter Film (1995)/Alter Film (1995) CD1.avi": 2, "Alter Film (1995)/Alter Film (1995) CD2.avi": 1, "Kopie/Alter Film (1995) CD1.avi": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Teile: %v", got)
	}
}

// DVD-Ordner (VIDEO_TS/VOB) mehrerer Discs und „1von3“-Teile sind keine
// Doppelgänger; dieselbe Disc an zweiter Stelle schon.
func TestTreeDupsDVD(t *testing.T) {
	root := t.TempDir()
	serien, filme := filepath.Join(root, "Serien"), filepath.Join(root, "Filme")
	files := []string{
		filepath.Join(filme, "altfilm", "1von3.mpg"),
		filepath.Join(filme, "altfilm", "3von3.mpg"),
		filepath.Join(filme, "Film X", "film.x.2020.mkv"),
		filepath.Join(filme, "Film X", "Sample", "film.x.2020-sample.mkv"),
	}
	for _, d := range []string{"SERIE_SEASON1_DISC_1", "SERIE_SEASON1_DISC_3", "SERIE_SEASON2_DISC_4"} {
		for _, v := range []string{"VTS_01_1.VOB", "VTS_01_2.VOB", "VTS_01_3.VOB"} {
			files = append(files, filepath.Join(serien, "Zeichentrick", d, "VIDEO_TS", v))
		}
	}
	files = append(files, filepath.Join(serien, "Zeichentrick", "Kopie", "SERIE_SEASON1_DISC_1", "VIDEO_TS", "VTS_01_1.VOB"))
	for _, p := range files {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	cfg := config.Defaults()
	cfg.Folders = []config.Folder{{Path: filme, Type: "movie"}, {Path: serien, Type: "tvshow"}}
	lib, _ := library.Open(filepath.Join(t.TempDir(), "lib.json"))
	if _, err := lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	s := &vs.Server{Lib: lib, Auth: &auth.Adapter{}}
	s.SetCfg(cfg)
	h := &Handler{S: s}
	yes := true
	rec := httptest.NewRecorder()
	h.tree(rec, httptest.NewRequest("GET", "/dsvr/api/tree", nil), auth.Session{Account: "admin", IsManager: &yes})
	var tr []treeRoot
	_ = json.Unmarshal(rec.Body.Bytes(), &tr)
	dups := map[string]int{}
	for _, r := range tr {
		for _, d := range r.Dirs {
			for _, f := range d.Files {
				if f.Dups > 1 {
					rel, _ := filepath.Rel(root, f.Path)
					dups[rel] = f.Dups
				}
			}
		}
	}
	want := map[string]int{
		"Filme/Film X/film.x.2020.mkv":                                         2,
		"Filme/Film X/Sample/film.x.2020-sample.mkv":                           2,
		"Serien/Zeichentrick/SERIE_SEASON1_DISC_1/VIDEO_TS/VTS_01_1.VOB":       2,
		"Serien/Zeichentrick/Kopie/SERIE_SEASON1_DISC_1/VIDEO_TS/VTS_01_1.VOB": 2,
	}
	if !reflect.DeepEqual(dups, want) {
		t.Fatalf("Doppelgänger: %v", dups)
	}
}

// TestDupIgnore: „Ignorieren“ nimmt eine Datei aus der Doppelgänger-Suche
// (gespeichert in der Konfiguration), „Wieder anzeigen“ macht es rückgängig.
func TestDupIgnore(t *testing.T) {
	root := t.TempDir()
	filme := filepath.Join(root, "Filme")
	main, sample := filepath.Join(filme, "Film X", "film.x.2020.mkv"), filepath.Join(filme, "Film X", "Sample", "film.x.2020-sample.mkv")
	extra := filepath.Join(filme, "Film X", "Kopie", "film.x.2020.mkv")
	for _, p := range []string{main, sample, extra} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	cfg := config.Defaults()
	cfg.Folders = []config.Folder{{Path: filme, Type: "movie"}}
	lib, _ := library.Open(filepath.Join(t.TempDir(), "lib.json"))
	if _, err := lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	s := &vs.Server{Lib: lib, Auth: &auth.Adapter{}}
	s.SetCfg(cfg)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	h := &Handler{S: s, CfgPath: cfgPath}
	yes, no := true, false
	admin := auth.Session{Account: "admin", IsManager: &yes}
	state := func() map[string][2]any {
		rec := httptest.NewRecorder()
		h.tree(rec, httptest.NewRequest("GET", "/dsvr/api/tree", nil), admin)
		var tr []treeRoot
		_ = json.Unmarshal(rec.Body.Bytes(), &tr)
		out := map[string][2]any{}
		for _, r := range tr {
			for _, d := range r.Dirs {
				for _, f := range d.Files {
					out[f.Path] = [2]any{f.Dups, f.Ignored}
				}
			}
		}
		return out
	}
	fileID := func(p string) int {
		for _, e := range lib.For(nil).FilesUnder(filme) {
			if e.File.Path == p {
				return e.File.ID
			}
		}
		t.Fatalf("%s fehlt", p)
		return 0
	}
	post := func(sess auth.Session, body string) int {
		rec := httptest.NewRecorder()
		h.dupIgnore(rec, httptest.NewRequest("POST", "/dsvr/api/dup-ignore", strings.NewReader(body)), sess)
		return rec.Code
	}
	if st := state(); st[main][0] != 3 || st[sample][0] != 3 {
		t.Fatalf("vorher: %v", st)
	}
	if code := post(auth.Session{Account: "gast", IsManager: &no}, `{"file_id":1,"ignore":true}`); code != 403 {
		t.Fatalf("Nicht-Admin: %d", code)
	}
	if code := post(admin, fmt.Sprintf(`{"file_id":%d,"ignore":true}`, fileID(sample))); code != 200 {
		t.Fatalf("ignorieren: %d", code)
	}
	st := state()
	if st[sample] != [2]any{0, true} || st[main][0] != 2 || st[extra][0] != 2 {
		t.Fatalf("nach Ignorieren: %v", st)
	}
	saved, err := config.Load(cfgPath)
	if err != nil || len(saved.DupIgnore) != 1 || saved.DupIgnore[0] != sample {
		t.Fatalf("gespeichert: %v %v", saved.DupIgnore, err)
	}
	post(admin, fmt.Sprintf(`{"file_id":%d,"ignore":true}`, fileID(extra)))
	if st := state(); st[main][0] != 1 || st[extra] != [2]any{0, true} {
		t.Fatalf("zwei ignoriert: %v", st)
	}
	post(admin, fmt.Sprintf(`{"file_id":%d,"ignore":false}`, fileID(sample)))
	post(admin, fmt.Sprintf(`{"file_id":%d,"ignore":false}`, fileID(extra)))
	if st := state(); st[main][0] != 3 || st[sample] != [2]any{3, false} || s.Cfg().DupIgnore != nil {
		t.Fatalf("rückgängig: %v %v", st, s.Cfg().DupIgnore)
	}
}

package vs

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
)

// TestRecentlyWatched: „Zuletzt gesehen“ (VS2 sort_by=watched, VS1
// recently_watched=1&sort_by=last_watched) zeigt nur angesehene Titel des
// Kontos, neueste zuerst – Filme und Serien.
func TestRecentlyWatched(t *testing.T) {
	ts, _, dir := newTestServer(t, "local")
	s := ts.Config.Handler.(*Server)
	serien := filepath.Join(dir, "serien")
	for _, p := range []string{"Dark/Season 01/Dark - S01E01.mkv", "Dark/Season 01/Dark - S01E02.mkv", "Lost/Season 01/Lost - S01E01.mkv"} {
		_ = os.MkdirAll(filepath.Join(serien, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(serien, p), []byte("x"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(dir, "media", "Film C (2019).mkv"), []byte("x"), 0o644) // nie angesehen
	cfg := *s.Cfg()
	cfg.Folders = append(cfg.Folders, config.Folder{Path: serien, Type: "tvshow"})
	s.SetCfg(cfg)
	if _, err := s.Lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	sid := login(t, ts.URL)
	list := func(api, key string, v url.Values) []string {
		t.Helper()
		v.Set("api", api)
		v.Set("method", "list")
		v.Set("_sid", sid)
		if v.Get("version") == "" {
			v.Set("version", "1")
		}
		m := call(t, ts.URL, v)
		if m["success"] != true {
			t.Fatalf("%s: %v", api, m)
		}
		var out []string
		for _, x := range m["data"].(map[string]any)[key].([]any) {
			out = append(out, x.(map[string]any)["title"].(string))
		}
		return out
	}
	watched := url.Values{"sort_by": {"watched"}, "sort_direction": {"desc"}}
	if got := list("SYNO.VideoStation2.Movie", "movie", url.Values{"sort_by": {"watched"}, "sort_direction": {"desc"}}); len(got) != 0 {
		t.Fatalf("ohne Wiedergabe: %v", got)
	}
	if got := list("SYNO.VideoStation2.TVShow", "tvshow", url.Values{"sort_by": {"watched"}, "sort_direction": {"desc"}}); len(got) != 0 {
		t.Fatalf("Serien ohne Wiedergabe: %v", got)
	}
	// Film A, danach Film B und eine Folge „Dark“ ansehen
	fileOf := func(title string) int {
		items, _ := s.Lib.Items(libraryQueryTitle(title))
		if len(items) == 0 || len(items[0].FileIDs) == 0 {
			t.Fatalf("%s fehlt", title)
		}
		return items[0].FileIDs[0]
	}
	set := func(fid int) {
		m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.WatchStatus"}, "version": {"1"}, "method": {"setinfo"}, "_sid": {sid}, "id": {strconv.Itoa(fid)}, "position": {"60"}})
		if m["success"] != true {
			t.Fatalf("setinfo: %v", m)
		}
		time.Sleep(1100 * time.Millisecond) // Zeitstempel in Sekunden
	}
	set(fileOf("Film A"))
	set(fileOf("Film B"))
	ep, _ := s.Lib.Items(libraryQueryEpisode("Dark"))
	set(ep[0].FileIDs[0])

	if got := list("SYNO.VideoStation2.Movie", "movie", watched); len(got) != 2 || got[0] != "Film B" || got[1] != "Film A" {
		t.Fatalf("Zuletzt gesehen (VS2): %v", got)
	}
	if got := list("SYNO.VideoStation.Movie", "movies", url.Values{"version": {"2"}, "recently_watched": {"1"}, "sort_by": {"last_watched"}, "sort_direction": {"desc"}}); len(got) != 2 || got[0] != "Film B" {
		t.Fatalf("Zuletzt gesehen (VS1): %v", got)
	}
	if got := list("SYNO.VideoStation2.TVShow", "tvshow", watched); len(got) != 1 || got[0] != "Dark" {
		t.Fatalf("Serien zuletzt gesehen: %v", got)
	}
	// Alle bzw. geöffnete Serie: unverändert vollständig
	if got := list("SYNO.VideoStation2.Movie", "movie", url.Values{"sort_by": {"title"}}); len(got) != 3 {
		t.Fatalf("Alle: %v", got)
	}
	if got := list("SYNO.VideoStation2.TVShow", "tvshow", url.Values{"sort_by": {"added"}, "sort_direction": {"desc"}}); len(got) != 2 {
		t.Fatalf("Serien zuletzt hinzugefügt: %v", got)
	}
	shows, _ := s.Lib.Shows(nil, 0, 0)
	var darkID int
	for _, sh := range shows {
		if sh.Title == "Dark" {
			darkID = sh.ID
		}
	}
	if got := list("SYNO.VideoStation2.TVShowEpisode", "episode", url.Values{"tvshow_id": {strconv.Itoa(darkID)}, "sort_by": {"watched"}}); len(got) != 2 {
		t.Fatalf("Folgen der Serie: %v", got)
	}
}

func libraryQueryTitle(title string) library.Query { return library.Query{Kind: "movie", Title: title} }
func libraryQueryEpisode(show string) library.Query {
	return library.Query{Kind: "episode", Title: show}
}

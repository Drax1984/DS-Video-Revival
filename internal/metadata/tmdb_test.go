package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
)

// fakeTMDb liefert Antworten im Format der TMDb API v3.
func fakeTMDb(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "KEY" && r.Header.Get("Authorization") == "" && !strings.HasPrefix(r.URL.Path, "/img") && !strings.HasPrefix(r.URL.Path, "/omdb") {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/movie":
			if r.URL.Query().Get("query") == "Inception" {
				w.Write([]byte(`{"results":[{"id":27205,"title":"Inception","release_date":"2010-07-15"}]}`))
				return
			}
			w.Write([]byte(`{"results":[]}`))
		case r.URL.Path == "/movie/27205":
			w.Write([]byte(`{"id":27205,"imdb_id":"tt1375666","title":"Inception","original_title":"Inception","tagline":"Dein Geist ist der Tatort","overview":"Cobb …","release_date":"2010-07-15","runtime":148,"genres":[{"name":"Action"},{"name":"Science Fiction"}],"vote_average":8.4,"poster_path":"/p.jpg","backdrop_path":"/b.jpg","credits":{"cast":[{"name":"Leonardo DiCaprio","order":0},{"name":"Elliot Page","order":2},{"name":"Joseph Gordon-Levitt","order":1}],"crew":[{"name":"Christopher Nolan","job":"Director","department":"Directing"},{"name":"Christopher Nolan","job":"Writer","department":"Writing"}]},"release_dates":{"results":[{"iso_3166_1":"DE","release_dates":[{"certification":"12"}]}]}}`))
		case r.URL.Path == "/search/tv":
			w.Write([]byte(`{"results":[{"id":70523}]}`))
		case r.URL.Path == "/tv/70523":
			w.Write([]byte(`{"id":70523,"name":"Dark","overview":"Winden …","first_air_date":"2017-12-01","genres":[{"name":"Drama"}],"vote_average":8.4,"poster_path":"/d.jpg","external_ids":{"imdb_id":"tt5753856"},"content_ratings":{"results":[{"iso_3166_1":"DE","rating":"16"}]},"credits":{"cast":[],"crew":[]}}`))
		case r.URL.Path == "/tv/70523/season/1/episode/1":
			w.Write([]byte(`{"id":1,"name":"Geheimnisse","overview":"2019 …","air_date":"2017-12-01","still_path":"/s.jpg","crew":[{"name":"Baran bo Odar","job":"Director"}]}`))
		case r.URL.Path == "/movie/267860":
			w.Write([]byte(`{"id":267860,"imdb_id":"tt3300542","title":"London Has Fallen","poster_path":null,"backdrop_path":"/lb.jpg","credits":{"cast":[],"crew":[]}}`))
		case r.URL.Path == "/movie/267860/images":
			w.Write([]byte(`{"posters":[{"file_path":"/lp.jpg","iso_639_1":"en"}]}`))
		case r.URL.Path == "/find/tt3300542":
			w.Write([]byte(`{"movie_results":[{"id":267860}],"tv_results":[]}`))
		case r.URL.Path == "/find/tt1375666":
			w.Write([]byte(`{"movie_results":[{"id":27205}],"tv_results":[]}`))
		case r.URL.Path == "/find/tt5753856":
			w.Write([]byte(`{"movie_results":[],"tv_results":[{"id":70523}]}`))
		case strings.HasPrefix(r.URL.Path, "/find/"):
			w.Write([]byte(`{"movie_results":[],"tv_results":[]}`))
		case strings.HasPrefix(r.URL.Path, "/img/"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1})
		case strings.HasPrefix(r.URL.Path, "/omdb"):
			w.Write([]byte(`{"imdbRating":"8.8"}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

func newClient(ts *httptest.Server, dir string) *Client {
	c := NewClient("KEY", "OMDB", "de-DE", dir)
	c.APIBase, c.ImageBase, c.OMDbBase = ts.URL, ts.URL+"/img", ts.URL+"/omdb"
	return c
}

func TestMovie(t *testing.T) {
	ts := fakeTMDb(t)
	defer ts.Close()
	c := newClient(ts, t.TempDir())
	m, err := c.Movie(context.Background(), "Inception", 2010, 0)
	if err != nil {
		t.Fatal(err)
	}
	if m.IMDbID != "tt1375666" || m.Certificate != "FSK 12" || m.IMDbRating != 8.8 || len(m.Genres) != 2 || m.Directors[0] != "Christopher Nolan" || m.Writers[0] != "Christopher Nolan" {
		t.Fatalf("%+v", m)
	}
	if m.Actors[1] != "Joseph Gordon-Levitt" {
		t.Errorf("Reihenfolge Darsteller: %v", m.Actors)
	}
	if _, err := os.Stat(m.Poster); err != nil {
		t.Errorf("Poster nicht gecacht: %v", err)
	}
	if _, err := c.Movie(context.Background(), "Gibtsnicht", 1999, 0); err != errNotFound {
		t.Errorf("erwartet nicht gefunden: %v", err)
	}
}

func TestEngineShowsAndEpisodes(t *testing.T) {
	ts := fakeTMDb(t)
	defer ts.Close()
	dir := t.TempDir()
	media := filepath.Join(dir, "m")
	os.MkdirAll(filepath.Join(media, "Filme", "Inception (2010)"), 0o755)
	os.MkdirAll(filepath.Join(media, "Serien", "Dark", "Season 01"), 0o755)
	os.WriteFile(filepath.Join(media, "Filme", "Inception (2010)", "Inception (2010).mkv"), nil, 0o644)
	os.WriteFile(filepath.Join(media, "Serien", "Dark", "Season 01", "Dark - S01E01.mkv"), nil, 0o644)
	lib, _ := library.Open(filepath.Join(dir, "lib.json"))
	lib.Scan([]config.Folder{{Path: filepath.Join(media, "Filme"), Type: "movie"}, {Path: filepath.Join(media, "Serien"), Type: "tvshow"}})
	t.Setenv("DSVR_FREE_API", ts.URL) // kein echter Netzzugriff für den IMDb-Datensatz
	e := NewEngine(lib)
	e.CacheDir = filepath.Join(dir, "cache")
	e.SetClient(newClient(ts, e.CacheDir))
	e.runOnce()
	e.runOnce() // Episoden brauchen die Serien-ID aus dem ersten Lauf nicht zwingend
	items, _ := lib.Items(library.Query{})
	for _, it := range items {
		if it.Meta == nil || it.Meta.NotFound {
			t.Errorf("%s ohne Metadaten", it.Title)
		}
	}
	shows, _ := lib.Shows(nil, 0, 0)
	if shows[0].Meta == nil || shows[0].Meta.IMDbID != "tt5753856" || shows[0].Meta.Certificate != "FSK 16" {
		t.Errorf("Serie: %+v", shows[0].Meta)
	}
	if s := e.Status(); s.Found < 3 {
		t.Errorf("Status %+v", s)
	}
	_ = time.Now
}

// TestAssignIMDbViaTMDb: IMDb-ID mit TMDb-Schlüssel wird über TMDb aufgelöst
// (mit Plakat und Hintergrund) – auch für Serien aus der Weboberfläche ("tvshow").
func TestAssignIMDbViaTMDb(t *testing.T) {
	ts := fakeTMDb(t)
	defer ts.Close()
	dir := t.TempDir()
	media := filepath.Join(dir, "m")
	os.MkdirAll(filepath.Join(media, "Filme", "Falsch (2010)"), 0o755)
	os.MkdirAll(filepath.Join(media, "Serien", "Dunkel", "Season 01"), 0o755)
	os.WriteFile(filepath.Join(media, "Filme", "Falsch (2010)", "Falsch (2010).mkv"), nil, 0o644)
	os.WriteFile(filepath.Join(media, "Serien", "Dunkel", "Season 01", "Dunkel - S01E01.mkv"), nil, 0o644)
	lib, _ := library.Open(filepath.Join(dir, "lib.json"))
	lib.Scan([]config.Folder{{Path: filepath.Join(media, "Filme"), Type: "movie"}, {Path: filepath.Join(media, "Serien"), Type: "tvshow"}})
	t.Setenv("DSVR_FREE_API", ts.URL)
	e := NewEngine(lib)
	e.CacheDir = filepath.Join(dir, "cache")
	e.SetClient(newClient(ts, e.CacheDir))
	movies, _ := lib.Items(library.Query{Kind: "movie"})
	m, err := e.Assign(context.Background(), "movie", movies[0].ID, " TT1375666 ")
	if err != nil {
		t.Fatal(err)
	}
	it, _ := lib.Item(movies[0].ID)
	if m.Source != "tmdb" || it.Meta == nil || it.Meta.TMDbID != 27205 || !it.Meta.Locked || it.PosterPath() == "" || it.BackdropPath() == "" {
		t.Fatalf("Film: %+v", it.Meta)
	}
	if _, err := os.Stat(it.PosterPath()); err != nil {
		t.Fatalf("Plakat fehlt: %v", err)
	}
	// TMDb ohne poster_path in der Detailabfrage → Plakat aus /images
	if _, err := e.Assign(context.Background(), "movie", movies[0].ID, "tt3300542"); err != nil {
		t.Fatal(err)
	}
	if it, _ := lib.Item(movies[0].ID); it.PosterPath() == "" || it.Meta.TMDbID != 267860 {
		t.Fatalf("Rückfall-Plakat fehlt: %+v", it.Meta)
	}
	shows, _ := lib.Shows(nil, 0, 0)
	if _, err := e.Assign(context.Background(), "tvshow", shows[0].ID, "tt5753856"); err != nil {
		t.Fatal(err)
	}
	sh, _ := lib.Show(shows[0].ID)
	if sh.Meta == nil || sh.Meta.TMDbID != 70523 || sh.Meta.Poster == "" {
		t.Fatalf("Serie: %+v", sh.Meta)
	}
}

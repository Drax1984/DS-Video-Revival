package metadata

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
)

// Fake-Dienste mit den live geprüften Antwortformen (Wikidata wbsearchentities/
// wbgetentities, Wikipedia REST summary, TVmaze, IMDb title.ratings.tsv.gz).
func fakeFree(t *testing.T) *httptest.Server {
	t.Helper()
	str := func(v string) map[string]any {
		return map[string]any{"mainsnak": map[string]any{"datavalue": map[string]any{"value": v}}, "rank": "normal"}
	}
	item := func(id string) map[string]any {
		return map[string]any{"mainsnak": map[string]any{"datavalue": map[string]any{"value": map[string]any{"id": id}}}, "rank": "normal"}
	}
	tm := func(v string) map[string]any {
		return map[string]any{"mainsnak": map[string]any{"datavalue": map[string]any{"value": map[string]any{"time": v, "precision": 11}}}, "rank": "normal"}
	}
	lbl := func(de string) map[string]any { return map[string]any{"de": map[string]any{"value": de}} }
	ents := map[string]any{
		// Buch gleichen Namens zuerst – muss übergangen werden
		"Q1": map[string]any{"id": "Q1", "labels": lbl("Der Pate"), "descriptions": map[string]any{"de": map[string]any{"value": "Roman von Mario Puzo"}},
			"claims": map[string]any{"P31": []any{item("Q7725634")}}},
		"Q47703": map[string]any{"id": "Q47703", "labels": lbl("Der Pate"),
			"descriptions": map[string]any{"de": map[string]any{"value": "Film von Francis Ford Coppola (1972)"}},
			"claims": map[string]any{
				"P31": []any{item("Q11424")}, "P345": []any{str("tt0068646")}, "P4947": []any{str("238")},
				"P577":  []any{tm("+1972-03-15T00:00:00Z")},
				"P2047": []any{map[string]any{"mainsnak": map[string]any{"datavalue": map[string]any{"value": map[string]any{"amount": "+175", "unit": "http://www.wikidata.org/entity/Q7727"}}}}},
				"P57":   []any{item("Q56094")}, "P136": []any{item("Q959790"), item("Q130232")}, "P1981": []any{item("Q20644797")},
			},
			"sitelinks": map[string]any{"dewiki": map[string]any{"title": "Der Pate (Film)"}, "enwiki": map[string]any{"title": "The Godfather"}}},
		// ohne Wikipedia-Artikel, aber mit Filmplakat auf Commons (P3383)
		"Q999": map[string]any{"id": "Q999", "labels": lbl("Metropolis"),
			"claims": map[string]any{"P31": []any{item("Q11424")}, "P345": []any{str("tt0017136")}, "P3383": []any{str("Metropolis poster.jpg")}}},
		"Q56094":    map[string]any{"id": "Q56094", "labels": lbl("Francis Ford Coppola")},
		"Q959790":   map[string]any{"id": "Q959790", "labels": lbl("Kriminalfilm")},
		"Q130232":   map[string]any{"id": "Q130232", "labels": lbl("Filmdrama")},
		"Q20644797": map[string]any{"id": "Q20644797", "labels": lbl("FSK 16")},
		"Q886":      map[string]any{"id": "Q886", "labels": lbl("Die Simpsons"), "claims": map[string]any{"P31": []any{item("Q5398426")}, "P345": []any{str("tt0096697")}}},
	}
	var ratings bytes.Buffer
	zw := gzip.NewWriter(&ratings)
	zw.Write([]byte("tconst\taverageRating\tnumVotes\ntt0000001\t5.7\t2000\ntt0068646\t9.2\t2100000\ntt0096697\t8.7\t440000\n"))
	zw.Close()
	png := []byte("\x89PNG\r\n\x1a\nfake")
	mux := http.NewServeMux()
	mux.HandleFunc("/wikidata", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.UserAgent(), "DSVideoRevival/") {
			t.Errorf("User-Agent fehlt: %q", r.UserAgent())
		}
		q := r.URL.Query()
		switch q.Get("action") {
		case "wbsearchentities":
			var res []any
			switch q.Get("search") {
			case "Der Pate":
				res = []any{map[string]any{"id": "Q1"}, map[string]any{"id": "Q47703"}}
			case "Die Simpsons":
				res = []any{map[string]any{"id": "Q886"}}
			}
			json.NewEncoder(w).Encode(map[string]any{"search": res})
		case "wbgetentities":
			out := map[string]any{}
			for _, id := range strings.Split(q.Get("ids"), "|") {
				if e, ok := ents[id]; ok {
					out[id] = e
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"entities": out})
		case "query":
			if q.Get("srsearch") == "haswbstatement:P345=tt0017136" {
				json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"search": []any{map[string]any{"title": "Q999"}}}})
				return
			}
			if q.Get("srsearch") == "haswbstatement:P345=tt0068646" {
				json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"search": []any{map[string]any{"title": "Q47703"}}}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"search": []any{}}})
		}
	})
	mux.HandleFunc("/wiki/de/api/rest_v1/page/summary/Der_Pate_(Film)", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"extract": "Der Pate ist ein US-amerikanischer Kriminalfilm.",
			"originalimage": map[string]any{"source": "http://" + r.Host + "/img/foto.png", "width": 300, "height": 400}})
	})
	mux.HandleFunc("/wiki/en/api/rest_v1/page/summary/The_Godfather", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"extract": "The Godfather is a 1972 film.",
			"originalimage": map[string]any{"source": "http://" + r.Host + "/img/Godfather_ver1.jpg", "width": 220, "height": 330}})
	})
	mux.HandleFunc("/commons/Metropolis_poster.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(png)
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(png)
	})
	mux.HandleFunc("/tvmaze/singlesearch/shows", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 83, "name": "The Simpsons", "premiered": "1989-12-17",
			"image": map[string]any{"original": "http://" + r.Host + "/img/simpsons.jpg"}, "summary": "<p>Springfield</p>",
			"rating": map[string]any{"average": 8.1}, "externals": map[string]any{"imdb": "tt0096697"}})
	})
	mux.HandleFunc("/tvmaze/shows/83/episodebynumber", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "name": "Simpsons Roasting on an Open Fire", "summary": "<p>Weihnachten</p>",
			"airdate": "1989-12-17", "runtime": 30, "image": map[string]any{"original": "http://" + r.Host + "/img/ep1.jpg"}})
	})
	mux.HandleFunc("/imdb/title.ratings.tsv.gz", func(w http.ResponseWriter, r *http.Request) { w.Write(ratings.Bytes()) })
	return httptest.NewServer(mux)
}

func TestFreeMovieShowEpisodeRatings(t *testing.T) {
	srv := fakeFree(t)
	defer srv.Close()
	t.Setenv("DSVR_FREE_API", srv.URL)
	dir := t.TempDir()
	f := NewFree("de-DE", filepath.Join(dir, "images"), dir)
	ctx := context.Background()

	m, err := f.Movie(ctx, "Der Pate", 1972)
	if err != nil {
		t.Fatal(err)
	}
	if m.IMDbID != "tt0068646" || m.TMDbID != 238 || m.Released != "1972-03-15" || m.Runtime != 175 {
		t.Fatalf("Grunddaten: %+v", m)
	}
	if m.Certificate != "FSK 16" || len(m.Directors) != 1 || m.Directors[0] != "Francis Ford Coppola" {
		t.Fatalf("Personen/FSK: %+v", m)
	}
	if strings.Join(m.Genres, ",") != "Kriminal,Filmdrama" {
		t.Fatalf("Genres: %v", m.Genres)
	}
	if m.Summary != "Der Pate ist ein US-amerikanischer Kriminalfilm." {
		t.Fatalf("deutscher Text erwartet: %q", m.Summary)
	}
	if m.Poster == "" || !strings.Contains(m.Poster, "Q47703") {
		t.Fatalf("Plakat aus en.wikipedia erwartet: %q", m.Poster)
	}
	if b, _ := os.ReadFile(m.Poster); len(b) == 0 {
		t.Fatal("Plakat nicht gespeichert")
	}

	s, err := f.Show(ctx, "Die Simpsons")
	if err != nil {
		t.Fatal(err)
	}
	if s.TVmazeID != 83 || s.IMDbID != "tt0096697" || !strings.Contains(s.Poster, "tvmaze-83") {
		t.Fatalf("Serie: %+v", s)
	}
	ep, err := f.Episode(ctx, s.TVmazeID, 1, 1)
	if err != nil || ep.Title != "Simpsons Roasting on an Open Fire" || ep.Summary != "Weihnachten" || ep.Backdrop == "" {
		t.Fatalf("Episode: %+v %v", ep, err)
	}

	r, err := f.IMDbRatings(ctx, map[string]bool{"tt0068646": true, "tt0096697": true, "tt9999999": true})
	if err != nil || r["tt0068646"] != 9.2 || r["tt0096697"] != 8.7 || len(r) != 2 {
		t.Fatalf("Bewertungen: %v %v", r, err)
	}

	byRef, err := f.ByRef(ctx, "movie", "tt0068646")
	if err != nil || byRef.Title != "Der Pate" {
		t.Fatalf("ByRef: %+v %v", byRef, err)
	}
	if mp, err := f.ByRef(ctx, "movie", "tt0017136"); err != nil || mp.Poster == "" {
		t.Fatalf("Plakat von Commons erwartet: %+v %v", mp, err)
	}
	if _, err := f.Movie(ctx, "Gibt es nicht", 0); err == nil {
		t.Fatal("unbekannter Titel muss fehlschlagen")
	}
}

func TestGenreName(t *testing.T) {
	for in, want := range map[string]string{"Kriminalfilm": "Kriminal", "Science-Fiction-Film": "Science-Fiction", "drama film": "Drama",
		"Film": "", "Komödie": "Komödie", "Filmdrama": "Filmdrama"} {
		if got := genreName(in); got != want {
			t.Errorf("%q → %q, erwartet %q", in, got, want)
		}
	}
}

func TestEngineWithoutKey(t *testing.T) {
	srv := fakeFree(t)
	defer srv.Close()
	t.Setenv("DSVR_FREE_API", srv.URL)
	dir := t.TempDir()
	media := filepath.Join(dir, "m")
	os.MkdirAll(filepath.Join(media, "Filme", "Der Pate (1972)"), 0o755)
	os.MkdirAll(filepath.Join(media, "Serien", "Die Simpsons", "Staffel 1"), 0o755)
	os.WriteFile(filepath.Join(media, "Filme", "Der Pate (1972)", "Der Pate (1972).mkv"), nil, 0o644)
	os.WriteFile(filepath.Join(media, "Serien", "Die Simpsons", "Staffel 1", "Die Simpsons - S01E01.mkv"), nil, 0o644)
	lib, _ := library.Open(filepath.Join(dir, "lib.json"))
	lib.Scan([]config.Folder{{Path: filepath.Join(media, "Filme"), Type: "movie"}, {Path: filepath.Join(media, "Serien"), Type: "tvshow"}})
	e := NewEngine(lib)
	e.CacheDir = filepath.Join(dir, "cache", "images")
	e.SetClient(NewClient("", "", "de-DE", e.CacheDir)) // kein Schlüssel
	e.runOnce()
	items, _ := lib.Items(library.Query{Kind: "movie"})
	if len(items) != 1 || items[0].Meta == nil || items[0].Meta.IMDbRating != 9.2 || items[0].PosterPath() == "" {
		t.Fatalf("Film: %+v", items[0].Meta)
	}
	shows, _ := lib.Shows(nil, 0, 0)
	if shows[0].Meta == nil || shows[0].Meta.IMDbRating != 8.7 || shows[0].Meta.TVmazeID != 83 {
		t.Fatalf("Serie: %+v", shows[0].Meta)
	}
	eps, _ := lib.Items(library.Query{Kind: "episode"})
	if eps[0].Meta == nil || eps[0].Meta.Title != "Simpsons Roasting on an Open Fire" {
		t.Fatalf("Episode: %+v", eps[0].Meta)
	}
	if st := e.Status(); st.Source != "frei" || st.Found != 3 || st.Ratings != 2 {
		t.Fatalf("Status: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "cache", "imdb-title.ratings.tsv.gz")); err != nil {
		t.Fatal("Datensatz nicht zwischengespeichert")
	}
}

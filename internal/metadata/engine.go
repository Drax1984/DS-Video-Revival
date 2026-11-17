package metadata

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/library"
)

// Engine fetches missing metadata in the background.
type Engine struct {
	Lib      *library.Index
	CacheDir string
	mu       sync.Mutex
	client   *Client
	free     *Free
	trigger  chan struct{}
	status   Status
}

// Status for the admin UI.
type Status struct {
	Running   bool   `json:"running"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	Found     int    `json:"found"`
	NotFound  int    `json:"not_found"`
	LastError string `json:"last_error,omitempty"`
	Enabled   bool   `json:"enabled"`
	Source    string `json:"source"`  // tmdb | frei
	Ratings   int    `json:"ratings"` // zuletzt aktualisierte IMDb-Bewertungen
	RatingErr string `json:"rating_error,omitempty"`
}

// NewEngine creates the engine; call Run in a goroutine.
func NewEngine(lib *library.Index) *Engine {
	return &Engine{Lib: lib, trigger: make(chan struct{}, 1)}
}

// SetClient sets the TMDb client. Ohne TMDb-Schlüssel wird die schlüsselfreie
// Quelle (Wikidata/Wikipedia/TVmaze + IMDb-Datensatz) verwendet.
func (e *Engine) SetClient(c *Client) {
	e.mu.Lock()
	e.client = c
	lang := "de-DE"
	if c != nil && c.Language != "" {
		lang = c.Language
	}
	dataDir := ""
	if e.CacheDir != "" {
		dataDir = filepath.Dir(e.CacheDir)
	}
	e.free = NewFree(lang, e.CacheDir, dataDir)
	e.status.Enabled = true
	e.status.Source = e.sourceLocked()
	e.mu.Unlock()
	e.Trigger()
}

func (e *Engine) sourceLocked() string {
	if e.client != nil && e.client.TMDbKey != "" {
		return "tmdb"
	}
	return "frei"
}

// Trigger requests a run.
func (e *Engine) Trigger() {
	select {
	case e.trigger <- struct{}{}:
	default:
	}
}

// Status returns a snapshot.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// Run processes triggers forever.
func (e *Engine) Run() {
	for range e.trigger {
		e.runOnce()
	}
}

func (e *Engine) runOnce() {
	e.mu.Lock()
	c, fr, src := e.client, e.free, e.sourceLocked()
	e.mu.Unlock()
	if fr == nil {
		return
	}
	targets := e.Lib.MetaTargets(7 * 24 * time.Hour)
	order := map[string]int{"show": 0, "movie": 1, "episode": 2}
	sort.SliceStable(targets, func(i, j int) bool { return order[targets[i].Kind] < order[targets[j].Kind] })
	e.mu.Lock()
	if len(targets) > 0 { // Zähler des letzten Laufs sonst stehen lassen
		e.status = Status{Running: true, Total: len(targets), Enabled: true, Source: src, Ratings: e.status.Ratings}
	}
	e.mu.Unlock()
	if len(targets) > 0 {
		log.Printf("metadata: %d Einträge abzugleichen (Quelle: %s)", len(targets), src)
	}
	for _, t := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		m, err := e.fetch(ctx, c, fr, src, t)
		cancel()
		e.mu.Lock()
		e.status.Done++
		switch {
		case err == nil:
			e.status.Found++
		case errors.Is(err, errNotFound):
			e.status.NotFound++
		case errors.Is(err, errSkip):
		default:
			e.status.LastError = err.Error()
		}
		e.mu.Unlock()
		if err != nil && !errors.Is(err, errNotFound) {
			if errors.Is(err, errSkip) {
				continue
			}
			log.Printf("metadata: %s %q: %v", t.Kind, t.Title, err)
			if err.Error() == "TMDb: API-Schlüssel ungültig" {
				break
			}
			continue
		}
		if m == nil {
			sname := "tmdb"
			if src == "frei" {
				sname = "wikidata"
			}
			m = &library.Meta{Source: sname, NotFound: true, Fetched: time.Now().Unix()}
		}
		if t.Kind == "show" {
			e.Lib.SetShowMeta(t.ID, m)
		} else {
			e.Lib.SetItemMeta(t.ID, m)
		}
		time.Sleep(200 * time.Millisecond) // höflich gegenüber den Diensten
	}
	// IMDb-Bewertungen aus dem offiziellen Datensatz (ohne Schlüssel).
	if want := e.Lib.IMDbIDsNeedingRating(7 * 24 * time.Hour); len(want) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		r, err := fr.IMDbRatings(ctx, want)
		cancel()
		n := e.Lib.SetIMDbRatings(r)
		e.mu.Lock()
		e.status.Ratings = n
		e.status.RatingErr = ""
		if err != nil {
			e.status.RatingErr = err.Error()
			log.Printf("metadata: IMDb-Bewertungen: %v", err)
		}
		e.mu.Unlock()
		if n > 0 {
			log.Printf("metadata: %d IMDb-Bewertungen aktualisiert", n)
		}
	}
	e.mu.Lock()
	e.status.Running = false
	e.mu.Unlock()
	if len(targets) > 0 {
		s := e.Status()
		log.Printf("metadata: fertig – %d gefunden, %d nicht gefunden", s.Found, s.NotFound)
	}
}

var errSkip = errors.New("übersprungen")

func (e *Engine) fetch(ctx context.Context, c *Client, fr *Free, src string, t library.MetaTarget) (*library.Meta, error) {
	switch t.Kind {
	case "movie":
		if src == "tmdb" {
			return c.Movie(ctx, t.Title, t.Year, 0)
		}
		return fr.Movie(ctx, t.Title, t.Year)
	case "show":
		if src == "tmdb" {
			return c.Show(ctx, t.Title, 0)
		}
		return fr.Show(ctx, t.Title)
	case "episode":
		sh, ok := e.Lib.Show(t.ShowID)
		if !ok || sh.Meta == nil {
			return nil, errSkip
		}
		if src == "tmdb" && sh.Meta.TMDbID != 0 && sh.Meta.Source == "tmdb" {
			return c.Episode(ctx, sh.Meta.TMDbID, t.Season, t.Episode)
		}
		if sh.Meta.TVmazeID != 0 {
			return fr.Episode(ctx, sh.Meta.TVmazeID, t.Season, t.Episode)
		}
		return nil, errSkip
	}
	return nil, errSkip
}

// Assign sets a manual match for an item or show (Korrektur). ref: TMDb-Nummer
// (mit TMDb-Schlüssel), IMDb-ID (tt…) oder Wikidata-ID (Q…).
func (e *Engine) Assign(ctx context.Context, kind string, id int, ref string) (*library.Meta, error) {
	e.mu.Lock()
	c, fr, src := e.client, e.free, e.sourceLocked()
	e.mu.Unlock()
	if fr == nil {
		return nil, errors.New("Metadaten nicht initialisiert")
	}
	ref = strings.TrimSpace(ref)
	if kind == "tvshow" { // Weboberfläche
		kind = "show"
	}
	byTMDb := func(n int, show bool) (*library.Meta, error) {
		if show {
			return c.Show(ctx, "", n)
		}
		return c.Movie(ctx, "", 0, n)
	}
	var m *library.Meta
	var err error
	switch n, convErr := strconv.Atoi(ref); {
	case convErr == nil && src == "tmdb":
		m, err = byTMDb(n, kind == "show")
	case src == "tmdb" && strings.HasPrefix(strings.ToLower(ref), "tt"):
		// IMDb-ID mit TMDb-Schlüssel: über TMDb auflösen (Plakat, Hintergrund,
		// deutsche Texte) – Wikidata/Wikipedia nur als Rückfall.
		id, show, ferr := c.FindIMDb(ctx, strings.ToLower(ref), kind == "show")
		if ferr == nil && show == (kind == "show") {
			m, err = byTMDb(id, show)
		} else {
			m, err = fr.ByRef(ctx, kind, ref)
		}
	default:
		m, err = fr.ByRef(ctx, kind, ref)
	}
	if err == nil && src == "tmdb" && m.Source != "tmdb" && m.TMDbID > 0 {
		// Treffer aus Wikidata mit TMDb-Nummer: Daten und Bilder von TMDb.
		if tm, terr := byTMDb(m.TMDbID, kind == "show"); terr == nil {
			m = tm
		}
	}
	if err != nil {
		return nil, err
	}
	m.Locked = true
	if m.IMDbID != "" {
		if r, rerr := fr.IMDbRatings(ctx, map[string]bool{m.IMDbID: true}); rerr == nil {
			if v, ok := r[m.IMDbID]; ok {
				m.IMDbRating, m.RatingFetched = v, time.Now().Unix()
			}
		}
	}
	if kind == "show" {
		e.Lib.SetShowMeta(id, m)
		items, _ := e.Lib.Items(library.Query{Kind: "episode", ShowID: id})
		for _, it := range items {
			e.Lib.SetItemMeta(it.ID, nil)
		}
		e.Trigger()
	} else {
		e.Lib.SetItemMeta(id, m)
	}
	return m, nil
}

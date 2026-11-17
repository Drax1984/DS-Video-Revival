package library

import "time"

// Meta: Metadaten eines Films, einer Serie oder Episode (Metadata Engine).
type Meta struct {
	Source        string   `json:"source"` // "tmdb" | "wikidata" | "tvmaze" | "manual" | "local"
	TMDbID        int      `json:"tmdb_id,omitempty"`
	IMDbID        string   `json:"imdb_id,omitempty"`
	Title         string   `json:"title,omitempty"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Tagline       string   `json:"tagline,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	Released      string   `json:"released,omitempty"` // YYYY-MM-DD
	Runtime       int      `json:"runtime,omitempty"`  // Minuten
	Genres        []string `json:"genres,omitempty"`
	Actors        []string `json:"actors,omitempty"`
	Directors     []string `json:"directors,omitempty"`
	Writers       []string `json:"writers,omitempty"`
	Rating        float64  `json:"rating,omitempty"`      // TMDb 0–10
	IMDbRating    float64  `json:"imdb_rating,omitempty"` // IMDb 0–10 (Datensatz oder OMDb)
	Certificate   string   `json:"certificate,omitempty"` // z.B. FSK 12
	Poster        string   `json:"poster,omitempty"`      // lokaler Cache-Pfad
	Backdrop      string   `json:"backdrop,omitempty"`
	Fetched       int64    `json:"fetched"`
	NotFound      bool     `json:"not_found,omitempty"`
	Locked        bool     `json:"locked,omitempty"` // manuell zugeordnet
	TVmazeID      int      `json:"tvmaze_id,omitempty"`
	RatingFetched int64    `json:"rating_fetched,omitempty"`
}

// MetaTarget is something that needs metadata.
type MetaTarget struct {
	Kind    string // movie | show | episode
	ID      int
	Title   string
	Year    int
	ShowID  int
	Season  int
	Episode int
	Meta    *Meta
}

// MetaTargets lists items/shows without metadata (or older than maxAge for retries of not-found).
func (ix *Index) MetaTargets(retryNotFound time.Duration) []MetaTarget {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	need := func(m *Meta) bool {
		if m == nil {
			return true
		}
		return m.NotFound && !m.Locked && time.Since(time.Unix(m.Fetched, 0)) > retryNotFound
	}
	var out []MetaTarget
	for _, s := range ix.data.Shows {
		if need(s.Meta) {
			out = append(out, MetaTarget{Kind: "show", ID: s.ID, Title: s.Title, Meta: s.Meta})
		}
	}
	for _, it := range ix.data.Items {
		switch it.Kind {
		case "movie":
			if need(it.Meta) {
				out = append(out, MetaTarget{Kind: "movie", ID: it.ID, Title: it.Title, Year: it.Year, Meta: it.Meta})
			}
		case "episode":
			if need(it.Meta) && it.Episode > 0 {
				out = append(out, MetaTarget{Kind: "episode", ID: it.ID, ShowID: it.ShowID, Season: it.Season, Episode: it.Episode, Meta: it.Meta})
			}
		}
	}
	return out
}

// SetItemMeta stores metadata for an item.
func (ix *Index) SetItemMeta(id int, m *Meta) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if it, ok := ix.data.Items[id]; ok {
		it.Meta = m
		_ = ix.saveLocked()
	}
}

// SetShowMeta stores metadata for a show.
func (ix *Index) SetShowMeta(id int, m *Meta) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if s, ok := ix.data.Shows[id]; ok {
		s.Meta = m
		_ = ix.saveLocked()
	}
}

// ClearMeta forces a refetch (all or one id).
func (ix *Index) ClearMeta(id int) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, it := range ix.data.Items {
		if (id == 0 || it.ID == id) && (it.Meta == nil || !it.Meta.Locked || id != 0) {
			it.Meta = nil
		}
	}
	for _, s := range ix.data.Shows {
		if (id == 0 || s.ID == id) && (s.Meta == nil || !s.Meta.Locked || id != 0) {
			s.Meta = nil
		}
	}
	_ = ix.saveLocked()
}

// PosterPath returns the best poster for an item (lokal vor TMDb-Cache).
// Nur gültige Bilddateien: ein fehlendes oder kaputtes lokales Plakat fällt
// auf das Plakat aus den Metadaten zurück.
func (it Item) PosterPath() string {
	if it.Poster != "" && ValidImage(it.Poster) {
		return it.Poster
	}
	if it.Meta != nil && ValidImage(it.Meta.Poster) {
		return it.Meta.Poster
	}
	return ""
}

// PosterPath: bestes gültiges Plakat einer Serie (lokal vor Metadaten).
func (sh Show) PosterPath() string {
	if sh.Poster != "" && ValidImage(sh.Poster) {
		return sh.Poster
	}
	if sh.Meta != nil && ValidImage(sh.Meta.Poster) {
		return sh.Meta.Poster
	}
	return ""
}

// BackdropPath returns the cached backdrop.
func (it Item) BackdropPath() string {
	if it.Meta != nil {
		return it.Meta.Backdrop
	}
	return ""
}

// DisplayTitle: von Hand gesetzter Titel, sonst Metadaten-Titel (nicht bei
// Episoden), sonst der aus dem Dateinamen erkannte Titel.
func (it Item) DisplayTitle() string {
	if it.CustomTitle != "" {
		return it.CustomTitle
	}
	if it.Meta != nil && it.Meta.Title != "" && it.Kind != "episode" {
		return it.Meta.Title
	}
	return it.Title
}

// EpisodeTitle: Titel einer Episode (von Hand, Metadaten, Dateiname).
func (it Item) EpisodeTitle() string {
	if it.CustomTitle != "" {
		return it.CustomTitle
	}
	if it.Meta != nil && it.Meta.Title != "" {
		return it.Meta.Title
	}
	return it.Title
}

// DisplayTitle einer Serie: von Hand, Metadaten, Ordnername.
func (s Show) DisplayTitle() string {
	if s.CustomTitle != "" {
		return s.CustomTitle
	}
	if s.Meta != nil && !s.Meta.NotFound && s.Meta.Title != "" {
		return s.Meta.Title
	}
	return s.Title
}

// SetTitle setzt (title != "") oder entfernt den von Hand gesetzten Titel.
func (ix *Index) SetTitle(kind string, id int, title string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if kind == "tvshow" {
		sh := ix.data.Shows[id]
		if sh == nil {
			return false
		}
		sh.CustomTitle = title
		sh.SortTitle = SortTitle(sh.DisplayTitle())
	} else {
		it := ix.data.Items[id]
		if it == nil {
			return false
		}
		it.CustomTitle = title
		if it.Kind != "episode" {
			it.SortTitle = SortTitle(it.DisplayTitle())
		}
	}
	_ = ix.saveLocked()
	return true
}

// RemoveFiles entfernt Dateien aus dem Index (nach dem Löschen auf der Platte);
// Objekte ohne Dateien und leere Serien verschwinden mit.
func (ix *Index) RemoveFiles(ids []int) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, id := range ids {
		if _, ok := ix.data.Files[id]; ok {
			ix.removeFileLocked(id)
		}
	}
	_ = ix.saveLocked()
}

// IMDbIDsNeedingRating lists IMDb IDs whose rating is missing or older than maxAge.
func (ix *Index) IMDbIDsNeedingRating(maxAge time.Duration) map[string]bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := map[string]bool{}
	add := func(m *Meta) {
		if m != nil && m.IMDbID != "" && (m.IMDbRating == 0 || time.Since(time.Unix(m.RatingFetched, 0)) > maxAge) {
			out[m.IMDbID] = true
		}
	}
	for _, it := range ix.data.Items {
		add(it.Meta)
	}
	for _, s := range ix.data.Shows {
		add(s.Meta)
	}
	return out
}

// SetIMDbRatings applies ratings by IMDb ID; returns the number of updated entries.
func (ix *Index) SetIMDbRatings(r map[string]float64) int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	n, now := 0, time.Now().Unix()
	set := func(m *Meta) {
		if m == nil || m.IMDbID == "" {
			return
		}
		if v, ok := r[m.IMDbID]; ok {
			m.IMDbRating, m.RatingFetched = v, now
			n++
		}
	}
	for _, it := range ix.data.Items {
		set(it.Meta)
	}
	for _, s := range ix.data.Shows {
		set(s.Meta)
	}
	if n > 0 {
		_ = ix.saveLocked()
	}
	return n
}

// Package metadata is the Metadata Engine (Komponente E).
//
// Quelle: TMDb API v3 (eigener API-Schlüssel des Nutzers; Nutzungsbedingungen:
// Namensnennung "This product uses the TMDB API but is not endorsed or certified
// by TMDB." in der Oberfläche). IMDb-Bewertung optional über OMDb (eigener
// Schlüssel). IMDb selbst bietet keine freie API; die IMDb-ID kommt von TMDb.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"dsvideorevival/internal/library"
)

// Client talks to TMDb and OMDb.
type Client struct {
	TMDbKey  string // v3-API-Key oder v4-Lesetoken
	OMDbKey  string
	Language string // z.B. de-DE
	Region   string // z.B. DE (Altersfreigabe)
	CacheDir string // Bilder
	HTTP     *http.Client
	// Basis-URLs (für Tests überschreibbar)
	APIBase   string
	ImageBase string
	OMDbBase  string
}

// NewClient with defaults.
func NewClient(tmdbKey, omdbKey, lang, cacheDir string) *Client {
	if lang == "" {
		lang = "de-DE"
	}
	region := "DE"
	if i := strings.Index(lang, "-"); i > 0 {
		region = strings.ToUpper(lang[i+1:])
	}
	c := &Client{TMDbKey: tmdbKey, OMDbKey: omdbKey, Language: lang, Region: region, CacheDir: cacheDir,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		APIBase: "https://api.themoviedb.org/3", ImageBase: "https://image.tmdb.org/t/p", OMDbBase: "https://www.omdbapi.com/"}
	// Nur für Tests/Entwicklung: alternative Endpunkte.
	if v := os.Getenv("DSVR_TMDB_API"); v != "" {
		c.APIBase, c.ImageBase, c.OMDbBase = v, v+"/img", v+"/omdb"
	}
	return c
}

// ErrNoKey: kein TMDb-Schlüssel konfiguriert.
var ErrNoKey = errors.New("kein TMDb-API-Schlüssel")

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if c.TMDbKey == "" {
		return ErrNoKey
	}
	if q == nil {
		q = url.Values{}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.APIBase+path, nil)
	if strings.Count(c.TMDbKey, ".") == 2 { // v4-Lesetoken (JWT)
		req.Header.Set("Authorization", "Bearer "+c.TMDbKey)
	} else {
		q.Set("api_key", c.TMDbKey)
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			return errors.New("TMDb: API-Schlüssel ungültig")
		case resp.StatusCode == http.StatusNotFound:
			return errNotFound
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("TMDb HTTP %d", resp.StatusCode)
		}
		return json.Unmarshal(body, out)
	}
	return errors.New("TMDb: Ratenbegrenzung")
}

var errNotFound = errors.New("nicht gefunden")

type credits struct {
	Cast []struct {
		Name  string `json:"name"`
		Order int    `json:"order"`
	} `json:"cast"`
	Crew []struct {
		Name       string `json:"name"`
		Job        string `json:"job"`
		Department string `json:"department"`
	} `json:"crew"`
}

func (cr credits) apply(m *library.Meta) {
	sort.SliceStable(cr.Cast, func(i, j int) bool { return cr.Cast[i].Order < cr.Cast[j].Order })
	for i, a := range cr.Cast {
		if i >= 15 {
			break
		}
		m.Actors = append(m.Actors, a.Name)
	}
	seen := map[string]bool{}
	for _, c := range cr.Crew {
		switch {
		case c.Job == "Director":
			m.Directors = appendUniq(m.Directors, c.Name, seen, "d")
		case c.Department == "Writing":
			m.Writers = appendUniq(m.Writers, c.Name, seen, "w")
		}
	}
}

func appendUniq(list []string, v string, seen map[string]bool, ns string) []string {
	if seen[ns+v] {
		return list
	}
	seen[ns+v] = true
	return append(list, v)
}

type genre struct {
	Name string `json:"name"`
}

func genreNames(g []genre) []string {
	var out []string
	for _, x := range g {
		out = append(out, x.Name)
	}
	return out
}

// Movie looks up a movie by title/year (or directly by TMDb ID if id > 0).
func (c *Client) Movie(ctx context.Context, title string, year, id int) (*library.Meta, error) {
	if id == 0 {
		var sr struct {
			Results []struct {
				ID          int     `json:"id"`
				Title       string  `json:"title"`
				ReleaseDate string  `json:"release_date"`
				Popularity  float64 `json:"popularity"`
			} `json:"results"`
		}
		q := url.Values{"query": {title}, "language": {c.Language}, "include_adult": {"false"}}
		if year > 0 {
			q.Set("year", strconv.Itoa(year))
		}
		if err := c.get(ctx, "/search/movie", q, &sr); err != nil {
			return nil, err
		}
		if len(sr.Results) == 0 && year > 0 { // Jahr kann um 1 abweichen
			q.Del("year")
			if err := c.get(ctx, "/search/movie", q, &sr); err != nil {
				return nil, err
			}
		}
		if len(sr.Results) == 0 {
			return nil, errNotFound
		}
		id = sr.Results[0].ID
	}
	var d struct {
		ID            int     `json:"id"`
		IMDbID        string  `json:"imdb_id"`
		Title         string  `json:"title"`
		OriginalTitle string  `json:"original_title"`
		Tagline       string  `json:"tagline"`
		Overview      string  `json:"overview"`
		ReleaseDate   string  `json:"release_date"`
		Runtime       int     `json:"runtime"`
		Genres        []genre `json:"genres"`
		VoteAverage   float64 `json:"vote_average"`
		PosterPath    string  `json:"poster_path"`
		BackdropPath  string  `json:"backdrop_path"`
		Credits       credits `json:"credits"`
		ReleaseDates  struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Dates   []struct {
					Certification string `json:"certification"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}
	if err := c.get(ctx, "/movie/"+strconv.Itoa(id), url.Values{"language": {c.Language}, "append_to_response": {"credits,release_dates"}}, &d); err != nil {
		return nil, err
	}
	m := &library.Meta{Source: "tmdb", TMDbID: d.ID, IMDbID: d.IMDbID, Title: d.Title, OriginalTitle: d.OriginalTitle,
		Tagline: d.Tagline, Summary: d.Overview, Released: d.ReleaseDate, Runtime: d.Runtime, Genres: genreNames(d.Genres),
		Rating: d.VoteAverage, Fetched: time.Now().Unix()}
	d.Credits.apply(m)
	for _, r := range d.ReleaseDates.Results {
		if r.Country == c.Region {
			for _, x := range r.Dates {
				if x.Certification != "" {
					m.Certificate = certLabel(c.Region, x.Certification)
					break
				}
			}
		}
	}
	if m.Summary == "" && !strings.HasPrefix(c.Language, "en") {
		var en struct {
			Overview string `json:"overview"`
		}
		if c.get(ctx, "/movie/"+strconv.Itoa(id), url.Values{"language": {"en-US"}}, &en) == nil {
			m.Summary = en.Overview
		}
	}
	m.Poster = c.image(ctx, d.PosterPath, "w500", fmt.Sprintf("movie-%d-poster", d.ID))
	if m.Poster == "" {
		m.Poster = c.anyPoster(ctx, "movie", d.ID, d.PosterPath)
	}
	m.Backdrop = c.image(ctx, d.BackdropPath, "w1280", fmt.Sprintf("movie-%d-backdrop", d.ID))
	c.imdbRating(ctx, m)
	return m, nil
}

// FindIMDb sucht einen Titel über seine IMDb-ID (TMDb /find). Ergebnis:
// TMDb-Nummer und ob es eine Serie ist.
func (c *Client) FindIMDb(ctx context.Context, imdbID string, show bool) (int, bool, error) {
	var r struct {
		Movies []struct {
			ID int `json:"id"`
		} `json:"movie_results"`
		TV []struct {
			ID int `json:"id"`
		} `json:"tv_results"`
	}
	if err := c.get(ctx, "/find/"+url.PathEscape(imdbID), url.Values{"external_source": {"imdb_id"}, "language": {c.Language}}, &r); err != nil {
		return 0, false, err
	}
	switch {
	case show && len(r.TV) > 0:
		return r.TV[0].ID, true, nil
	case !show && len(r.Movies) > 0:
		return r.Movies[0].ID, false, nil
	case len(r.Movies) > 0:
		return r.Movies[0].ID, false, nil
	case len(r.TV) > 0:
		return r.TV[0].ID, true, nil
	}
	return 0, false, errNotFound
}

// Show looks up a TV show.
func (c *Client) Show(ctx context.Context, title string, id int) (*library.Meta, error) {
	if id == 0 {
		var sr struct {
			Results []struct {
				ID int `json:"id"`
			} `json:"results"`
		}
		if err := c.get(ctx, "/search/tv", url.Values{"query": {title}, "language": {c.Language}}, &sr); err != nil {
			return nil, err
		}
		if len(sr.Results) == 0 {
			return nil, errNotFound
		}
		id = sr.Results[0].ID
	}
	var d struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		OriginalName string  `json:"original_name"`
		Tagline      string  `json:"tagline"`
		Overview     string  `json:"overview"`
		FirstAirDate string  `json:"first_air_date"`
		Genres       []genre `json:"genres"`
		VoteAverage  float64 `json:"vote_average"`
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
		Credits      credits `json:"credits"`
		ExternalIDs  struct {
			IMDbID string `json:"imdb_id"`
		} `json:"external_ids"`
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
	}
	if err := c.get(ctx, "/tv/"+strconv.Itoa(id), url.Values{"language": {c.Language}, "append_to_response": {"credits,external_ids,content_ratings"}}, &d); err != nil {
		return nil, err
	}
	m := &library.Meta{Source: "tmdb", TMDbID: d.ID, IMDbID: d.ExternalIDs.IMDbID, Title: d.Name, OriginalTitle: d.OriginalName,
		Tagline: d.Tagline, Summary: d.Overview, Released: d.FirstAirDate, Genres: genreNames(d.Genres), Rating: d.VoteAverage, Fetched: time.Now().Unix()}
	d.Credits.apply(m)
	for _, r := range d.ContentRatings.Results {
		if r.Country == c.Region && r.Rating != "" {
			m.Certificate = certLabel(c.Region, r.Rating)
		}
	}
	m.Poster = c.image(ctx, d.PosterPath, "w500", fmt.Sprintf("tv-%d-poster", d.ID))
	if m.Poster == "" {
		m.Poster = c.anyPoster(ctx, "tv", d.ID, d.PosterPath)
	}
	m.Backdrop = c.image(ctx, d.BackdropPath, "w1280", fmt.Sprintf("tv-%d-backdrop", d.ID))
	c.imdbRating(ctx, m)
	return m, nil
}

// Episode looks up one episode of a known show.
func (c *Client) Episode(ctx context.Context, showTMDb, season, episode int) (*library.Meta, error) {
	var d struct {
		ID          int     `json:"id"`
		Name        string  `json:"name"`
		Overview    string  `json:"overview"`
		AirDate     string  `json:"air_date"`
		Runtime     int     `json:"runtime"`
		VoteAverage float64 `json:"vote_average"`
		StillPath   string  `json:"still_path"`
		Crew        []struct {
			Name       string `json:"name"`
			Job        string `json:"job"`
			Department string `json:"department"`
		} `json:"crew"`
		GuestStars []struct {
			Name string `json:"name"`
		} `json:"guest_stars"`
	}
	p := fmt.Sprintf("/tv/%d/season/%d/episode/%d", showTMDb, season, episode)
	if err := c.get(ctx, p, url.Values{"language": {c.Language}}, &d); err != nil {
		return nil, err
	}
	m := &library.Meta{Source: "tmdb", TMDbID: d.ID, Title: d.Name, Summary: d.Overview, Released: d.AirDate,
		Runtime: d.Runtime, Rating: d.VoteAverage, Fetched: time.Now().Unix()}
	seen := map[string]bool{}
	for _, cr := range d.Crew {
		if cr.Job == "Director" {
			m.Directors = appendUniq(m.Directors, cr.Name, seen, "d")
		} else if cr.Department == "Writing" {
			m.Writers = appendUniq(m.Writers, cr.Name, seen, "w")
		}
	}
	for i, g := range d.GuestStars {
		if i < 10 {
			m.Actors = append(m.Actors, g.Name)
		}
	}
	m.Backdrop = c.image(ctx, d.StillPath, "w780", fmt.Sprintf("ep-%d-still", d.ID))
	return m, nil
}

// anyPoster: Rückfall, wenn die Detailabfrage kein (ladbares) Plakat lieferte –
// erstes Plakat aus /images (Deutsch, Englisch, ohne Sprache).
func (c *Client) anyPoster(ctx context.Context, kind string, id int, skip string) string {
	var r struct {
		Posters []struct {
			FilePath string `json:"file_path"`
		} `json:"posters"`
	}
	if c.get(ctx, "/"+kind+"/"+strconv.Itoa(id)+"/images", url.Values{"include_image_language": {"de,en,null"}}, &r) != nil {
		return ""
	}
	for i, p := range r.Posters {
		if i >= 3 {
			break
		}
		if p.FilePath == "" || p.FilePath == skip {
			continue
		}
		if dst := c.image(ctx, p.FilePath, "w500", fmt.Sprintf("%s-%d-poster", kind, id)); dst != "" {
			return dst
		}
	}
	return ""
}

// image downloads and caches an image; returns the local path or "".
func (c *Client) image(ctx context.Context, path, size, name string) string {
	if path == "" || c.CacheDir == "" {
		return ""
	}
	ext := filepath.Ext(path)
	if ext == "" {
		ext = ".jpg"
	}
	dst := filepath.Join(c.CacheDir, name+ext)
	if library.ValidImage(dst) {
		return dst
	}
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ { // ein zweiter Versuch bei Netzfehlern
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.ImageBase+"/"+size+path, nil)
		r, err := c.HTTP.Do(req)
		if err == nil && r.StatusCode == http.StatusOK {
			resp = r
			break
		}
		if err == nil {
			r.Body.Close()
		}
		if ctx.Err() != nil {
			return ""
		}
	}
	if resp == nil {
		return ""
	}
	defer resp.Body.Close()
	_ = os.MkdirAll(c.CacheDir, 0o750)
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return ""
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 20<<20))
	f.Close()
	if err != nil || !library.ValidImage(tmp) { // nur echte Bilder übernehmen
		os.Remove(tmp)
		return ""
	}
	if os.Rename(tmp, dst) != nil {
		return ""
	}
	return dst
}

// imdbRating fills IMDbRating via OMDb (optional).
func (c *Client) imdbRating(ctx context.Context, m *library.Meta) {
	if c.OMDbKey == "" || m.IMDbID == "" {
		return
	}
	u := c.OMDbBase + "?" + url.Values{"i": {m.IMDbID}, "apikey": {c.OMDbKey}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var r struct {
		IMDbRating string `json:"imdbRating"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r) == nil {
		if v, err := strconv.ParseFloat(r.IMDbRating, 64); err == nil {
			m.IMDbRating = v
		}
	}
}

func certLabel(region, c string) string {
	if region == "DE" {
		if _, err := strconv.Atoi(c); err == nil {
			return "FSK " + c
		}
	}
	return c
}

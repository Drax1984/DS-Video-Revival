package metadata

// Schlüsselfreie Metadaten (Standard, wenn kein TMDb-Schlüssel hinterlegt ist):
//
//   Wikidata (CC0)      Abgleich Titel/Jahr, IMDb-/TMDb-ID, Regie, Drehbuch,
//                       Darsteller, Genre, FSK, Laufzeit, Erscheinungsdatum
//   Wikipedia (CC BY-SA) Inhaltszusammenfassung (de, sonst en), Plakatbild
//                       (meist en.wikipedia, lokal zwischengespeichert)
//   TVmaze (CC BY-SA)   Serienposter, Episodentitel/-bilder
//   IMDb-Datensätze     IMDb-Bewertung (title.ratings.tsv.gz, nicht-kommerzielle
//                       Nutzung erlaubt: „Information courtesy of IMDb“)
//
// Formate live geprüft (2026-11-09) über wbsearchentities/wbgetentities und
// /api/rest_v1/page/summary.

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/library"
)

// Free is the keyless metadata source.
type Free struct {
	Lang      string // "de"
	CacheDir  string // Bilder
	DataDir   string // IMDb-Datensatz
	HTTP      *http.Client
	Wikidata  string
	Wikipedia string // Muster mit %s für Sprache, z.B. https://%s.wikipedia.org
	TVmaze    string
	Commons   string // Wikimedia Commons, Special:FilePath/
	IMDbData  string

	ratingsMu sync.Mutex
}

// UserAgent gemäß Wikimedia-Richtlinie (eindeutig, ohne persönliche Daten).
const UserAgent = "DSVideoRevival/0.3 (Synology NAS media server; +https://www.wikidata.org/wiki/Wikidata:Data_access)"

// NewFree creates the keyless client.
func NewFree(lang, cacheDir, dataDir string) *Free {
	if i := strings.Index(lang, "-"); i > 0 {
		lang = lang[:i]
	}
	if lang == "" {
		lang = "de"
	}
	f := &Free{Lang: lang, CacheDir: cacheDir, DataDir: dataDir, HTTP: &http.Client{Timeout: 25 * time.Second},
		Wikidata: "https://www.wikidata.org/w/api.php", Wikipedia: "https://%s.wikipedia.org", TVmaze: "https://api.tvmaze.com",
		Commons:  "https://commons.wikimedia.org/wiki/Special:FilePath/",
		IMDbData: "https://datasets.imdbws.com/title.ratings.tsv.gz"}
	if v := os.Getenv("DSVR_FREE_API"); v != "" { // nur Tests
		f.Wikidata, f.Wikipedia, f.TVmaze, f.IMDbData = v+"/wikidata", v+"/wiki/%s", v+"/tvmaze", v+"/imdb/title.ratings.tsv.gz"
		f.Commons = v + "/commons/"
	}
	return f
}

func (f *Free) getJSON(ctx context.Context, u string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := f.HTTP.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 503:
			time.Sleep(time.Duration(attempt+1) * 3 * time.Second)
			continue
		case resp.StatusCode == http.StatusNotFound:
			return errNotFound
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("HTTP %d von %s", resp.StatusCode, req.URL.Host)
		}
		return json.Unmarshal(body, out)
	}
	return errors.New("Ratenbegrenzung")
}

// ---------- Wikidata ----------

type wdValue struct {
	DataValue *struct {
		Value json.RawMessage `json:"value"`
	} `json:"datavalue"`
}

type wdEntity struct {
	ID     string `json:"id"`
	Labels map[string]struct {
		Value string `json:"value"`
	} `json:"labels"`
	Descriptions map[string]struct {
		Value string `json:"value"`
	} `json:"descriptions"`
	Claims map[string][]struct {
		Mainsnak wdValue `json:"mainsnak"`
		Rank     string  `json:"rank"`
	} `json:"claims"`
	Sitelinks map[string]struct {
		Title string `json:"title"`
	} `json:"sitelinks"`
}

func (e wdEntity) label(lang string) string {
	if l, ok := e.Labels[lang]; ok {
		return l.Value
	}
	if l, ok := e.Labels["en"]; ok {
		return l.Value
	}
	return ""
}

func (e wdEntity) strings(p string) []string {
	var out []string
	for _, c := range e.Claims[p] {
		if c.Mainsnak.DataValue == nil || c.Rank == "deprecated" {
			continue
		}
		var s string
		if json.Unmarshal(c.Mainsnak.DataValue.Value, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func (e wdEntity) items(p string) []string {
	var out []string
	for _, c := range e.Claims[p] {
		if c.Mainsnak.DataValue == nil || c.Rank == "deprecated" {
			continue
		}
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(c.Mainsnak.DataValue.Value, &v) == nil && v.ID != "" {
			out = append(out, v.ID)
		}
	}
	return out
}

// dates: alle P577-Daten (frühestes zuerst, Format YYYY-MM-DD).
func (e wdEntity) dates(p string) []string {
	var out []string
	for _, c := range e.Claims[p] {
		if c.Mainsnak.DataValue == nil {
			continue
		}
		var v struct {
			Time      string `json:"time"`
			Precision int    `json:"precision"`
		}
		if json.Unmarshal(c.Mainsnak.DataValue.Value, &v) != nil || len(v.Time) < 11 {
			continue
		}
		d := strings.TrimPrefix(v.Time, "+")[:10]
		if v.Precision < 11 {
			d = d[:4] + "-01-01"
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (e wdEntity) amount(p string) float64 {
	for _, c := range e.Claims[p] {
		if c.Mainsnak.DataValue == nil {
			continue
		}
		var v struct {
			Amount string `json:"amount"`
		}
		if json.Unmarshal(c.Mainsnak.DataValue.Value, &v) == nil {
			n, _ := strconv.ParseFloat(strings.TrimPrefix(v.Amount, "+"), 64)
			return n
		}
	}
	return 0
}

// Klassen „Film“ bzw. „Fernsehserie“ (P31). Beschreibungstext als Rückfall.
var filmClasses = map[string]bool{"Q11424": true, "Q24869": true, "Q506240": true, "Q202866": true, "Q229390": true, "Q93204": true,
	"Q17123180": true, "Q24862": true, "Q226730": true, "Q18089587": true, "Q20667187": true, "Q1054574": true}
var seriesClasses = map[string]bool{"Q5398426": true, "Q1259759": true, "Q581714": true, "Q526877": true, "Q117467246": true, "Q63952888": true, "Q15416": true}

func (f *Free) search(ctx context.Context, title string) ([]string, error) {
	var r struct {
		Search []struct {
			ID string `json:"id"`
		} `json:"search"`
	}
	q := url.Values{"action": {"wbsearchentities"}, "search": {title}, "language": {f.Lang}, "uselang": {f.Lang}, "type": {"item"}, "limit": {"10"}, "format": {"json"}}
	if err := f.getJSON(ctx, f.Wikidata+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range r.Search {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

func (f *Free) entities(ctx context.Context, ids []string, props string) (map[string]wdEntity, error) {
	out := map[string]wdEntity{}
	for len(ids) > 0 {
		n := len(ids)
		if n > 50 {
			n = 50
		}
		var r struct {
			Entities map[string]wdEntity `json:"entities"`
		}
		q := url.Values{"action": {"wbgetentities"}, "ids": {strings.Join(ids[:n], "|")}, "props": {props},
			"languages": {f.Lang + "|en"}, "sitefilter": {f.Lang + "wiki|enwiki"}, "format": {"json"}}
		if err := f.getJSON(ctx, f.Wikidata+"?"+q.Encode(), &r); err != nil {
			return nil, err
		}
		for k, v := range r.Entities {
			out[k] = v
		}
		ids = ids[n:]
	}
	return out, nil
}

// match: bester Treffer passender Klasse; Jahr (±1) bevorzugt.
func (f *Free) match(ctx context.Context, title string, year int, series bool) (*wdEntity, error) {
	ids, err := f.search(ctx, title)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errNotFound
	}
	ents, err := f.entities(ctx, ids, "claims|labels|descriptions|sitelinks")
	if err != nil {
		return nil, err
	}
	var best *wdEntity
	bestScore := -1
	for i, id := range ids {
		e, ok := ents[id]
		if !ok {
			continue
		}
		classOK := false
		for _, c := range e.items("P31") {
			if (!series && filmClasses[c]) || (series && seriesClasses[c]) {
				classOK = true
			}
		}
		desc := strings.ToLower(e.Descriptions[f.Lang].Value + " " + e.Descriptions["en"].Value)
		if !classOK {
			if series {
				classOK = strings.Contains(desc, "serie") || strings.Contains(desc, "series")
			} else {
				classOK = strings.Contains(desc, "film") && !strings.Contains(desc, "reihe") && !strings.Contains(desc, "trilog") && !strings.Contains(desc, "series")
			}
		}
		if !classOK {
			continue
		}
		score := 100 - i*5
		if year > 0 {
			for _, d := range e.dates("P577") {
				if y, _ := strconv.Atoi(d[:4]); y >= year-1 && y <= year+1 {
					score += 50
					break
				}
			}
			for _, d := range e.dates("P580") { // Serien: Startzeitpunkt
				if y, _ := strconv.Atoi(d[:4]); y >= year-1 && y <= year+1 {
					score += 50
					break
				}
			}
		}
		if len(e.strings("P345")) > 0 {
			score += 10
		}
		if score > bestScore {
			ec := e
			best, bestScore = &ec, score
		}
	}
	if best == nil {
		return nil, errNotFound
	}
	return best, nil
}

func (f *Free) labels(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	ents, err := f.entities(ctx, ids, "labels")
	if err != nil {
		return out
	}
	for id, e := range ents {
		out[id] = e.label(f.Lang)
	}
	return out
}

// ---------- Wikipedia ----------

type wpSummary struct {
	Extract       string `json:"extract"`
	OriginalImage *struct {
		Source string `json:"source"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"originalimage"`
}

func (f *Free) summary(ctx context.Context, lang, title string) (*wpSummary, error) {
	var s wpSummary
	u := fmt.Sprintf(f.Wikipedia, lang) + "/api/rest_v1/page/summary/" + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	if err := f.getJSON(ctx, u, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// download speichert ein Bild im Cache.
func (f *Free) download(ctx context.Context, src, name string) string {
	if src == "" || f.CacheDir == "" {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(strings.SplitN(src, "?", 2)[0]))
	if ext != ".png" && ext != ".webp" {
		ext = ".jpg"
	}
	dst := filepath.Join(f.CacheDir, name+ext)
	if library.ValidImage(dst) {
		return dst
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return ""
	}
	_ = os.MkdirAll(f.CacheDir, 0o750)
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return ""
	}
	_, err = io.Copy(out, io.LimitReader(resp.Body, 20<<20))
	out.Close()
	if err != nil || !library.ValidImage(tmp) || os.Rename(tmp, dst) != nil {
		os.Remove(tmp)
		return ""
	}
	return dst
}

var reParens = regexp.MustCompile(`\s*\([^)]*\)`)

// fill: gemeinsamer Teil für Film und Serie.
func (f *Free) fill(ctx context.Context, e *wdEntity, kind string) *library.Meta {
	m := &library.Meta{Source: "wikidata", Title: e.label(f.Lang), Fetched: time.Now().Unix()}
	if ids := e.strings("P345"); len(ids) > 0 {
		m.IMDbID = ids[0]
	}
	tmdbProp := "P4947"
	if kind == "show" {
		tmdbProp = "P4983"
	}
	if ids := e.strings(tmdbProp); len(ids) > 0 {
		m.TMDbID, _ = strconv.Atoi(ids[0])
	}
	if d := e.dates("P577"); len(d) > 0 {
		m.Released = d[0]
	} else if d := e.dates("P580"); len(d) > 0 {
		m.Released = d[0]
	}
	if n := e.amount("P2047"); n > 0 {
		m.Runtime = int(n)
	}
	directors, writers, cast, genres, fsk := e.items("P57"), e.items("P58"), e.items("P161"), e.items("P136"), e.items("P1981")
	if len(cast) > 12 {
		cast = cast[:12]
	}
	all := append(append(append(append(append([]string{}, directors...), writers...), cast...), genres...), fsk...)
	lab := f.labels(ctx, all)
	pick := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			if l := lab[id]; l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	m.Directors, m.Writers, m.Actors = pick(directors), pick(writers), pick(cast)
	seen := map[string]bool{}
	for _, g := range pick(genres) {
		if g = genreName(g); g != "" && !seen[g] && len(m.Genres) < 4 {
			seen[g] = true
			m.Genres = append(m.Genres, g)
		}
	}
	for _, c := range pick(fsk) {
		if strings.HasPrefix(c, "FSK") {
			m.Certificate = c
		}
	}
	// Inhalt in der eingestellten Sprache, sonst englisch. Plakat aus der
	// englischen Wikipedia (Infobox-Bild = Kinoplakat); andere Sprachversionen
	// zeigen wegen fehlender Fair-Use-Regel meist Fotos statt Plakate.
	if sl, ok := e.Sitelinks[f.Lang+"wiki"]; ok && f.Lang != "en" {
		if s, err := f.summary(ctx, f.Lang, sl.Title); err == nil {
			m.Summary = s.Extract
		}
	}
	if sl, ok := e.Sitelinks["enwiki"]; ok {
		if s, err := f.summary(ctx, "en", sl.Title); err == nil {
			if m.Summary == "" {
				m.Summary = s.Extract
			}
			if s.OriginalImage != nil && s.OriginalImage.Height >= s.OriginalImage.Width {
				m.Poster = f.download(ctx, s.OriginalImage.Source, kind+"-"+e.ID+"-poster")
			}
		}
	}
	// Rückfall: Filmplakat (P3383) aus Wikimedia Commons, z.B. wenn die
	// englische Wikipedia kein Plakat zeigt.
	if files := e.strings("P3383"); m.Poster == "" && len(files) > 0 && f.Commons != "" {
		m.Poster = f.download(ctx, f.Commons+url.PathEscape(strings.ReplaceAll(files[0], " ", "_"))+"?width=500", kind+"-"+e.ID+"-poster")
	}
	return m
}

// Movie looks up a movie keylessly.
func (f *Free) Movie(ctx context.Context, title string, year int) (*library.Meta, error) {
	e, err := f.match(ctx, title, year, false)
	if errors.Is(err, errNotFound) && reParens.MatchString(title) {
		e, err = f.match(ctx, reParens.ReplaceAllString(title, ""), year, false)
	}
	if err != nil {
		return nil, err
	}
	return f.fill(ctx, e, "movie"), nil
}

func genreName(g string) string {
	g = strings.TrimSpace(g)
	low := strings.ToLower(g)
	switch {
	case low == "film" || low == "film genre" || low == "filmgenre" || low == "spielfilm" || low == "fernsehserie":
		return ""
	case strings.HasSuffix(low, " film"):
		g = g[:len(g)-5]
	case strings.HasSuffix(low, "-film"):
		g = g[:len(g)-5]
	case strings.HasSuffix(low, "film") && len(g) > 6:
		g = g[:len(g)-4]
	}
	g = strings.TrimSpace(strings.TrimSuffix(g, "-"))
	if g == "" {
		return ""
	}
	return strings.ToUpper(g[:1]) + g[1:]
}

// ByRef: manuelle Zuordnung über Wikidata-ID (Q…), IMDb-ID (tt…) oder TMDb-Nummer.
func (f *Free) ByRef(ctx context.Context, kind, ref string) (*library.Meta, error) {
	ref = strings.TrimSpace(ref)
	var qid string
	switch {
	case len(ref) > 1 && (ref[0] == 'Q' || ref[0] == 'q'):
		qid = strings.ToUpper(ref)
	default:
		prop := "P345"
		if _, err := strconv.Atoi(ref); err == nil {
			prop = "P4947"
			if kind == "show" {
				prop = "P4983"
			}
		} else if !strings.HasPrefix(ref, "tt") {
			return nil, errors.New("Kennung nicht erkannt (erwartet tt…, Q… oder TMDb-Nummer)")
		}
		var r struct {
			Query struct {
				Search []struct {
					Title string `json:"title"`
				} `json:"search"`
			} `json:"query"`
		}
		q := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {"haswbstatement:" + prop + "=" + ref}, "srlimit": {"1"}, "format": {"json"}}
		if err := f.getJSON(ctx, f.Wikidata+"?"+q.Encode(), &r); err != nil {
			return nil, err
		}
		if len(r.Query.Search) == 0 {
			return nil, errNotFound
		}
		qid = r.Query.Search[0].Title
	}
	ents, err := f.entities(ctx, []string{qid}, "claims|labels|descriptions|sitelinks")
	if err != nil {
		return nil, err
	}
	e, ok := ents[qid]
	if !ok || len(e.Claims) == 0 {
		return nil, errNotFound
	}
	if kind == "show" {
		m := f.fill(ctx, &e, "show")
		var t tvmShow
		if m.IMDbID != "" && f.getJSON(ctx, f.TVmaze+"/lookup/shows?imdb="+url.QueryEscape(m.IMDbID), &t) == nil {
			m.TVmazeID = t.ID
			if t.Image != nil {
				if p := f.download(ctx, t.Image.Original, fmt.Sprintf("tvmaze-%d-poster", t.ID)); p != "" {
					m.Poster = p
				}
			}
		}
		return m, nil
	}
	return f.fill(ctx, &e, "movie"), nil
}

// ---------- Serien: Wikidata + TVmaze ----------

type tvmShow struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Image *struct {
		Medium   string `json:"medium"`
		Original string `json:"original"`
	} `json:"image"`
	Summary   string `json:"summary"`
	Premiered string `json:"premiered"`
	Genres    []string
	Rating    struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Externals struct {
		IMDb string `json:"imdb"`
	} `json:"externals"`
}

var reTags = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string { return strings.TrimSpace(reTags.ReplaceAllString(s, "")) }

// Show looks up a TV show (Wikidata für deutsche Texte, TVmaze für Bilder).
func (f *Free) Show(ctx context.Context, title string) (*library.Meta, error) {
	var m *library.Meta
	if e, err := f.match(ctx, title, 0, true); err == nil {
		m = f.fill(ctx, e, "show")
	}
	var t tvmShow
	err := errNotFound
	if m != nil && m.IMDbID != "" { // genauer: über die IMDb-ID aus Wikidata
		err = f.getJSON(ctx, f.TVmaze+"/lookup/shows?imdb="+url.QueryEscape(m.IMDbID), &t)
	}
	if err != nil {
		err = f.getJSON(ctx, f.TVmaze+"/singlesearch/shows?"+url.Values{"q": {title}}.Encode(), &t)
	}
	if err != nil && m == nil {
		return nil, err
	}
	if err == nil {
		if m == nil {
			m = &library.Meta{Source: "tvmaze", Title: t.Name, Released: t.Premiered, Genres: t.Genres, Summary: stripHTML(t.Summary), Fetched: time.Now().Unix()}
		}
		if m.IMDbID == "" {
			m.IMDbID = t.Externals.IMDb
		}
		if t.Image != nil { // TVmaze-Poster haben Vorrang (echte Serienplakate)
			if p := f.download(ctx, t.Image.Original, fmt.Sprintf("tvmaze-%d-poster", t.ID)); p != "" {
				m.Poster = p
			}
		}
		if m.Rating == 0 {
			m.Rating = t.Rating.Average
		}
		m.TVmazeID = t.ID
	}
	return m, nil
}

// Episode from TVmaze (Titel, Inhalt, Standbild; meist englisch).
func (f *Free) Episode(ctx context.Context, tvmazeID, season, episode int) (*library.Meta, error) {
	if tvmazeID == 0 {
		return nil, errNotFound
	}
	var ep struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Summary string `json:"summary"`
		Airdate string `json:"airdate"`
		Runtime int    `json:"runtime"`
		Image   *struct {
			Original string `json:"original"`
		} `json:"image"`
	}
	u := fmt.Sprintf("%s/shows/%d/episodebynumber?season=%d&number=%d", f.TVmaze, tvmazeID, season, episode)
	if err := f.getJSON(ctx, u, &ep); err != nil {
		return nil, err
	}
	m := &library.Meta{Source: "tvmaze", Title: ep.Name, Summary: stripHTML(ep.Summary), Released: ep.Airdate, Runtime: ep.Runtime, Fetched: time.Now().Unix()}
	if ep.Image != nil {
		m.Backdrop = f.download(ctx, ep.Image.Original, fmt.Sprintf("tvmaze-ep-%d", ep.ID))
	}
	return m, nil
}

// ---------- IMDb-Bewertungen ----------

// IMDbRatings lädt (höchstens wöchentlich) title.ratings.tsv.gz und liefert
// die Bewertungen der gesuchten IDs. Speicher: nur die gesuchten Einträge.
func (f *Free) IMDbRatings(ctx context.Context, want map[string]bool) (map[string]float64, error) {
	f.ratingsMu.Lock()
	defer f.ratingsMu.Unlock()
	out := map[string]float64{}
	if len(want) == 0 {
		return out, nil
	}
	path := filepath.Join(f.DataDir, "imdb-title.ratings.tsv.gz")
	if st, err := os.Stat(path); err != nil || time.Since(st.ModTime()) > 7*24*time.Hour {
		if err := f.fetchFile(ctx, f.IMDbData, path); err != nil && !fileOK(path) {
			return out, err
		}
	}
	fh, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return out, err
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		line := sc.Text()
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 || !want[line[:tab]] {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			if v, err := strconv.ParseFloat(parts[1], 64); err == nil {
				out[parts[0]] = v
			}
		}
	}
	return out, sc.Err()
}

func fileOK(p string) bool { st, err := os.Stat(p); return err == nil && st.Size() > 0 }

func (f *Free) fetchFile(ctx context.Context, src, dst string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	req.Header.Set("User-Agent", UserAgent)
	cl := &http.Client{Timeout: 5 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("IMDb-Datensatz HTTP %d", resp.StatusCode)
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o750)
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(resp.Body, 200<<20))
	out.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

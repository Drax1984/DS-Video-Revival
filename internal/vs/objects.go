package vs

// Antwortobjekte exakt nach den Datenmodellen der Original-App
// DS video 3.4.7 (com.synology.dsvideo, Gson-Klassen VideoVo, VideoV2Vo,
// VideoVo.FileVo, Video2ListVo, VideoListV2Vo). Beleg: docs/protocol.md, Q9.
//
//   VideoV2Vo:  id, title, tagline, original_available, season, episode,
//               tvshow_id, tvshow_mapper_id, mapper_id, type, last_watched,
//               certificate, rating (int, -1 = keine, Anzeige rating/10),
//               tvshow_backdrop_mtime, additional
//   Additional: actor/director/writer/genre: String[]; summary: String;
//               extra: String (JSON, siehe extraJSON); poster_mtime,
//               backdrop_mtime: String (null = kein Bild); watched_ratio: float;
//               collection: Liste; is_parental_controlled: bool; file: FileVo[]
//   FileVo:     id, path, sharepath, filesize, container_type, duration
//               ("HH:MM:SS", App ruft duration.split(":") OHNE Null-Prüfung auf),
//               resolutionx, resolutiony, video_codec (ohne Null-Prüfung
//               verglichen), audio_codec: String; audio_bitrate, video_bitrate,
//               frame_bitrate: long; channel: int; conversion_produced: bool

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/webapi"
)

// knownAdditional: Schlüssel, deren Typ aus der App belegt ist.
var knownAdditional = map[string]bool{"watched_ratio": true, "file": true, "summary": true, "actor": true,
	"director": true, "writer": true, "genre": true, "collection": true, "poster_mtime": true,
	"backdrop_mtime": true, "extra": true, "conversion_produced": true, "parental_control": true}

func recordAdditional(c *Ctx) []string {
	add := c.Req.StrList("additional")
	for _, a := range add {
		if !knownAdditional[a] {
			c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, "additional:"+a, nil)
		}
	}
	return add
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func originalAvailable(year int) string {
	if year <= 0 {
		return ""
	}
	return strconv.Itoa(year) + "-01-01"
}

func hms(sec float64) string {
	s := int(sec + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}

func mtimeOf(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	return st.ModTime().Format("2006-01-02 15:04:05.000000"), true
}

// setNames: Namensliste (actor/director/writer/genre) setzen. Die Android-App
// baut daraus Text mit substring(0, len-2|3) (VideoItem.Additional.get*String)
// und stürzt bei einer LEEREN Liste ab (StringIndexOutOfBounds); ein fehlender
// Schlüssel (null) wird dort sauber ausgeblendet. Für Android daher leere
// Listen weglassen; andere Clients (iOS) erhalten wie bisher [].
func setNames(c *Ctx, a map[string]any, k string, v []string) {
	if len(v) == 0 && isAndroid(c) {
		return
	}
	a[k] = strs(v)
}

func isAndroid(c *Ctx) bool {
	return c != nil && c.Req != nil && c.Req.HTTP != nil && strings.Contains(c.Req.HTTP.UserAgent(), "Android")
}

func strs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// extraJSON baut den extra-Text, den die App mit Gson in VideoVo.Extra parst:
// {"com.synology.TheMovieDb":{"reference":{"imdb":…,"themoviedb":…},"rating":{"themoviedb":7.6}}}
func extraJSON(m *library.Meta) string {
	if m == nil || m.NotFound {
		return ""
	}
	ref := map[string]any{}
	if m.IMDbID != "" {
		ref["imdb"] = m.IMDbID
	}
	if m.TMDbID != 0 && m.Source == "tmdb" {
		ref["themoviedb"] = strconv.Itoa(m.TMDbID)
	}
	rating := m.Rating
	if m.IMDbRating > 0 {
		rating = m.IMDbRating
	}
	db := map[string]any{"reference": ref}
	if rating > 0 {
		db["rating"] = map[string]any{"themoviedb": rating}
	}
	b, _ := json.Marshal(map[string]any{"com.synology.TheMovieDb": db})
	return string(b)
}

// ratingInt: App zeigt rating/10 (0–10) bzw. /2 als Sterne; -1 = keine.
func ratingInt(m *library.Meta) int {
	if m == nil || m.NotFound {
		return -1
	}
	r := m.Rating
	if m.IMDbRating > 0 {
		r = m.IMDbRating
	}
	if r <= 0 {
		return -1
	}
	return int(math.Round(r * 10))
}

// videoObject: gemeinsamer Aufbau für Film, Episode, Heimvideo.
func videoObject(c *Ctx, it library.Item, add []string, probe bool) map[string]any {
	o := map[string]any{
		"id":                 it.ID,
		"mapper_id":          it.ID,
		"title":              it.DisplayTitle(),
		"sort_title":         library.SortTitle(it.DisplayTitle()),
		"tagline":            "",
		"original_available": originalAvailable(it.Year),
		"certificate":        "",
		"rating":             ratingInt(it.Meta),
		"create_time":        it.AddedAt,
		"last_watched":       c.S.lastWatched(c.Sess.Account, it),
		"library_id":         it.LibraryID,
		"metadata_locked":    false,
	}
	if it.Kind == "tv_record" {
		// Felder laut App-Modell VideoVo (channel_name; Datum über
		// original_available, das die App vor record_date auswertet).
		o["channel_name"] = it.Channel
		if len(it.Recorded) >= 10 {
			o["original_available"] = it.Recorded[:10]
		}
	}
	if m := it.Meta; m != nil && !m.NotFound {
		o["certificate"], o["tagline"], o["metadata_locked"] = m.Certificate, m.Tagline, m.Locked
		if m.Released != "" {
			o["original_available"] = m.Released
		}
	}
	if it.Kind == "episode" {
		// Episoden: title = Serientitel, tagline = Episodentitel (Video-Info-Plugin-Guide).
		o["title"], o["tagline"] = it.Show, it.EpisodeTitle()
		o["season"], o["episode"] = it.Season, it.Episode
		o["tvshow_id"], o["tvshow_mapper_id"] = it.ShowID, it.ShowID
		if sh, ok := c.Lib.Show(it.ShowID); ok {
			o["title"] = sh.DisplayTitle() // wie in der Serienliste
			if t, ok := mtimeOf(showImage(sh, true)); ok {
				o["tvshow_backdrop_mtime"] = t
			}
		}
	}
	o["additional"] = additionalFor(c, it, add, probe)
	return o
}

// movieObject bleibt als Name für bestehende Aufrufer.
func movieObject(c *Ctx, it library.Item, add []string, probe bool) map[string]any {
	return videoObject(c, it, add, probe)
}

func additionalFor(c *Ctx, it library.Item, add []string, probe bool) map[string]any {
	a := map[string]any{}
	m := it.Meta
	if m != nil && m.NotFound {
		m = nil
	}
	for _, k := range add {
		switch k {
		case "summary":
			a[k] = ""
			if m != nil {
				a[k] = m.Summary
			}
		case "actor", "director", "writer", "genre":
			var v []string
			if m != nil {
				v = map[string][]string{"actor": m.Actors, "director": m.Directors, "writer": m.Writers, "genre": m.Genres}[k]
			}
			setNames(c, a, k, v)
		case "collection":
			a[k] = []any{}
		case "extra":
			a[k] = extraJSON(m)
		case "parental_control":
			a["is_parental_controlled"] = false
		case "poster_mtime":
			if t, ok := mtimeOf(it.PosterPath()); ok && c.S.Cfg().SendPosterMtime {
				a[k] = t
			}
		case "backdrop_mtime":
			if t, ok := mtimeOf(it.BackdropPath()); ok && c.S.Cfg().SendPosterMtime {
				a[k] = t
			}
		case "watched_ratio":
			a[k] = c.S.watchedRatio(c.Sess.Account, it)
			if r := a[k].(float64); r > 0 && c.Req.Method == "getinfo" {
				log.Printf("wiedergabe: „%s“ zu %.1f %% gesehen gemeldet (Konto %q)", it.Title, r*100, c.Sess.Account)
			}
		case "file":
			a[k] = fileObjects(c, it, probe)
		}
	}
	return a
}

func fileObjects(c *Ctx, it library.Item, probe bool) []map[string]any {
	files := []map[string]any{}
	for _, f := range c.Lib.Files(it) {
		if (f.Probe.NeedsProbe(c.S.FFprobe) || f.Probe.NeedsProfiles(c.S.FFprobe)) && probe {
			f.Probe = library.RunProbe(c.S.FFprobe, f.Path)
			c.Lib.SetProbe(f.ID, f.Probe)
		}
		files = append(files, fileObject(f, false))
	}
	return files
}

// fileObject: Dateiobjekt (App-Modell VideoVo.FileVo).
func fileObject(f library.File, produced bool) map[string]any {
	pr := f.Probe
	if pr == nil {
		pr = &library.Probe{}
	}
	return map[string]any{
		"id":                  strconv.Itoa(f.ID),
		"path":                f.Path,
		"sharepath":           sharePath(f.Path),
		"filesize":            strconv.FormatInt(f.Size, 10),
		"container_type":      strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), "."),
		"duration":            hms(pr.Duration),
		"resolutionx":         strconv.Itoa(pr.Width),
		"resolutiony":         strconv.Itoa(pr.Height),
		"video_codec":         pr.VideoCodec,
		"audio_codec":         pr.AudioCodec,
		"video_bitrate":       pr.VideoBitrate,
		"audio_bitrate":       pr.AudioBitrate,
		"frame_bitrate":       pr.Bitrate,
		"channel":             pr.Channels,
		"conversion_produced": produced,
	}
}

// sharePath: /volume1/video/x.mkv → /video/x.mkv
func sharePath(p string) string {
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)
	if len(parts) == 2 && strings.HasPrefix(parts[0], "volume") {
		return "/" + parts[1]
	}
	return p
}

func (s *Server) watchedRatio(account string, it library.Item) float64 {
	best := 0.0
	for _, f := range s.Lib.Files(it) {
		ws, ok := s.Lib.Watch(account, f.ID)
		if !ok || f.Probe == nil || f.Probe.Duration <= 0 {
			continue
		}
		r := float64(ws.Position) / f.Probe.Duration
		if r > 1 {
			r = 1
		}
		best = math.Max(best, r)
	}
	return math.Round(best*1000) / 1000
}

func (s *Server) lastWatched(account string, it library.Item) int64 {
	var last int64
	for _, f := range s.Lib.Files(it) {
		if ws, ok := s.Lib.Watch(account, f.ID); ok && ws.UpdatedAt > last {
			last = ws.UpdatedAt
		}
	}
	return last
}

// ---------- Listen ----------

func movieList(key string) handlerFunc {
	return func(c *Ctx) {
		add := recordAdditional(c)
		q := listQuery(c, "movie")
		if c.Req.Has("keyword") { // Suche
			q.Title = c.Req.Str("keyword")
		}
		items, total := c.Lib.Items(q)
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			out = append(out, videoObject(c, it, add, false))
		}
		webapi.OK(c.W, map[string]any{key: out, "offset": q.Offset, "total": total})
	}
}

// itemGet: getinfo für Film/Episode/Heimvideo (id = "[N]" oder N).
func itemGet(key, kind string) handlerFunc {
	return func(c *Ctx) {
		add := recordAdditional(c)
		out := []map[string]any{}
		for _, id := range c.Req.IntList("id") {
			if it, ok := c.Lib.Item(id); ok && (kind == "" || it.Kind == kind) {
				out = append(out, videoObject(c, it, add, true))
				// Diagnose Fortsetzen: was die App anfragt und was gespeichert ist.
				var pos []string
				for _, f := range c.Lib.Files(it) {
					ws, _ := c.Lib.Watch(c.Sess.Account, f.ID)
					dur := 0.0
					if f.Probe != nil {
						dur = f.Probe.Duration
					}
					pos = append(pos, fmt.Sprintf("Datei %d: %ds von %.0fs", f.ID, ws.Position, dur))
				}
				log.Printf("wiedergabe: Detail „%s“ (id %d, Konto %q) additional=%v, %s", it.Title, it.ID, c.Sess.Account, add, strings.Join(pos, "; "))
			}
		}
		if len(out) == 0 {
			webapi.Fail(c.W, webapi.ErrUnknown)
			return
		}
		webapi.OK(c.W, map[string]any{key: out, "offset": 0, "total": len(out)})
	}
}

func movieGet(key string) handlerFunc { return itemGet(key, "movie") }

func emptyList(key string) handlerFunc {
	return func(c *Ctx) {
		webapi.OK(c.W, map[string]any{key: []any{}, "offset": c.Req.Int("offset", 0), "total": 0})
	}
}

func showImage(sh library.Show, backdrop bool) string {
	if backdrop {
		if sh.Meta != nil {
			return sh.Meta.Backdrop
		}
		return ""
	}
	return sh.PosterPath()
}

func showObject(c *Ctx, sh library.Show, add []string) map[string]any {
	title := sh.Title
	o := map[string]any{"id": sh.ID, "mapper_id": sh.ID, "title": title, "sort_title": library.SortTitle(title),
		"original_available": "", "library_id": sh.LibraryID, "create_time": sh.AddedAt, "metadata_locked": false,
		"certificate": "", "rating": ratingInt(sh.Meta), "tagline": ""}
	if t := sh.DisplayTitle(); t != title {
		o["title"], o["sort_title"] = t, library.SortTitle(t)
	}
	if m := sh.Meta; m != nil && !m.NotFound {
		o["original_available"], o["certificate"], o["tagline"] = m.Released, m.Certificate, m.Tagline
	}
	a := map[string]any{}
	for _, k := range add {
		switch k {
		case "summary":
			a[k] = ""
			if sh.Meta != nil {
				a[k] = sh.Meta.Summary
			}
		case "poster_mtime":
			if t, ok := mtimeOf(showImage(sh, false)); ok {
				a[k] = t
			}
		case "backdrop_mtime":
			if t, ok := mtimeOf(showImage(sh, true)); ok {
				a[k] = t
			}
		case "extra":
			a[k] = extraJSON(sh.Meta)
		case "actor", "director", "writer", "genre":
			var v []string
			if m := sh.Meta; m != nil {
				v = map[string][]string{"actor": m.Actors, "director": m.Directors, "writer": m.Writers, "genre": m.Genres}[k]
			}
			setNames(c, a, k, v)
		case "collection":
			a[k] = []any{}
		case "watched_ratio":
			a[k] = 0
		}
	}
	o["additional"] = a
	return o
}

func showList(key string) handlerFunc {
	return func(c *Ctx) {
		add := recordAdditional(c)
		q := library.ShowQuery{LibraryID: libraryParam(c), Offset: c.Req.Int("offset", 0), Limit: c.Req.Int("limit", 0),
			SortBy: map[string]string{"added": "added", "date": "date", "year": "date"}[c.Req.Str("sort_by")],
			Desc:   strings.EqualFold(c.Req.Str("sort_direction"), "desc")}
		if q.Limit < 0 {
			q.Limit = 0
		}
		if recentlyWatched(c) { // „Zuletzt gesehen“: Serien mit angesehenen Folgen
			q.SortBy, q.WatchedBy = "watched", c.Sess.Account
			if !c.Req.Has("sort_direction") {
				q.Desc = true
			}
		}
		shows, total := c.Lib.ShowsQ(q)
		out := make([]map[string]any, 0, len(shows))
		for _, sh := range shows {
			out = append(out, showObject(c, sh, add))
		}
		webapi.OK(c.W, map[string]any{key: out, "offset": c.Req.Int("offset", 0), "total": total})
	}
}

func showGet(key string) handlerFunc {
	return func(c *Ctx) {
		add := recordAdditional(c)
		out := []map[string]any{}
		for _, id := range c.Req.IntList("id") {
			if sh, ok := c.Lib.Show(id); ok {
				out = append(out, showObject(c, sh, add))
			}
		}
		if len(out) == 0 {
			webapi.Fail(c.W, webapi.ErrUnknown)
			return
		}
		webapi.OK(c.W, map[string]any{key: out, "offset": 0, "total": len(out)})
	}
}

func episodeList(key string) handlerFunc {
	return func(c *Ctx) {
		add := recordAdditional(c)
		q := listQuery(c, "episode")
		q.ShowID = c.Req.Int("tvshow_id", 0)
		if q.ShowID != 0 && q.WatchedBy != "" { // geöffnete Serie: immer alle Folgen
			q.WatchedBy, q.SortBy = "", ""
		}
		if q.SortBy == "" || q.SortBy == "title" {
			q.SortBy = ""
		}
		items, total := c.Lib.Items(q)
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			out = append(out, videoObject(c, it, add, false))
		}
		webapi.OK(c.W, map[string]any{key: out, "offset": q.Offset, "total": total})
	}
}

// recordingList: TV-Aufnahmen (VS1 „recordings“, VS2 „recording“). Die
// Android-App filtert die Gruppenansichten mit channel_name=["…"] bzw.
// title=["…"] (FetchRecordingVideoListTask) – beides genau, nicht als Suche.
func recordingList(key string) handlerFunc {
	return func(c *Ctx) {
		add := recordAdditional(c)
		q := listQuery(c, "tv_record")
		channel, hasChannel := jsonFirst(c, "channel_name")
		program, hasProgram := jsonFirst(c, "title")
		if hasChannel || hasProgram {
			q.Title, q.Offset, q.Limit = "", 0, 0
		}
		items, total := c.Lib.Items(q)
		if hasChannel || hasProgram {
			f := items[:0]
			for _, it := range items {
				if (!hasChannel || it.Channel == channel) && (!hasProgram || it.DisplayTitle() == program) {
					f = append(f, it)
				}
			}
			items, total = pageItems(f, c.Req.Int("offset", 0), c.Req.Int("limit", 0)), len(f)
		}
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			out = append(out, videoObject(c, it, add, false))
		}
		webapi.OK(c.W, map[string]any{key: out, "offset": c.Req.Int("offset", 0), "total": total})
	}
}

// recordingGroups: list_channel / list_program (App-Modell RecordingGroupVo:
// data.channel[] bzw. data.program[] mit title und preview_video).
func recordingGroups(key string, byChannel bool) handlerFunc {
	return func(c *Ctx) {
		items, _ := c.Lib.Items(library.Query{Kind: "tv_record", LibraryID: libraryParam(c)})
		seen := map[string]bool{}
		var titles []string
		for _, it := range items {
			t := it.DisplayTitle()
			if byChannel {
				t = it.Channel
			}
			if t == "" || seen[t] {
				continue // Aufnahmen ohne erkannten Sender erscheinen nur unter „Alle“
			}
			seen[t] = true
			titles = append(titles, t)
		}
		sort.Slice(titles, func(i, j int) bool { return strings.ToLower(titles[i]) < strings.ToLower(titles[j]) })
		total := len(titles)
		off, lim := c.Req.Int("offset", 0), c.Req.Int("limit", 0)
		if off > total {
			off = total
		}
		titles = titles[off:]
		if lim > 0 && lim < len(titles) {
			titles = titles[:lim]
		}
		groups := make([]map[string]any, 0, len(titles))
		for _, t := range titles {
			groups = append(groups, map[string]any{"title": t, "preview_video": []any{}})
		}
		webapi.OK(c.W, map[string]any{key: groups, "offset": off, "total": total})
	}
}

// jsonFirst: erster Eintrag eines Listenparameters (["x"]) oder Text.
func jsonFirst(c *Ctx, k string) (string, bool) {
	if !c.Req.Has(k) {
		return "", false
	}
	raw := strings.TrimSpace(c.Req.Params.Get(k))
	if strings.HasPrefix(raw, "[") {
		if l := c.Req.StrList(k); len(l) > 0 {
			return l[0], true
		}
		return "", false
	}
	return c.Req.Str(k), raw != ""
}

func pageItems(in []library.Item, off, lim int) []library.Item {
	if off < 0 {
		off = 0
	}
	if off > len(in) {
		off = len(in)
	}
	in = in[off:]
	if lim > 0 && lim < len(in) {
		in = in[:lim]
	}
	return in
}

func homeList(key string) handlerFunc {
	return func(c *Ctx) {
		add := recordAdditional(c)
		q := listQuery(c, "home_video")
		items, total := c.Lib.Items(q)
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			out = append(out, videoObject(c, it, add, false))
		}
		webapi.OK(c.W, map[string]any{key: out, "offset": q.Offset, "total": total})
	}
}

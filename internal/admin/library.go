package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/vs"
)

const mediaCookie = "dsvr_media"

// setMediaCookie: Bilder/Videos im Browser (img/video-Tags können keine
// Header senden) → HttpOnly-Cookie, nur für GET-Medienpfade gültig.
func setMediaCookie(w http.ResponseWriter, r *http.Request, sid string) {
	if c, err := r.Cookie(mediaCookie); err == nil && c.Value == sid {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: mediaCookie, Value: sid, Path: "/dsvr/media/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
}

// serveMedia: /dsvr/media/img?… (Cookie) und /dsvr/media/raw/<token> (Token).
func (h *Handler) serveMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/dsvr/media/raw/") {
		tok := strings.TrimPrefix(r.URL.Path, "/dsvr/media/raw/")
		if i := strings.Index(tok, "/"); i >= 0 {
			tok = tok[:i]
		}
		h.mu.Lock()
		p, ok := h.raw[tok]
		h.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if key, blocked := h.S.Conns().Stream(tok); blocked {
			http.Error(w, "Konto ist auf einem anderen Gerät aktiv", http.StatusForbidden)
			return
		} else if key != "" {
			gone := h.S.Conns().Begin(key, true)
			defer h.S.Conns().End(key, true)
			w = vs.GuardWriter(w, gone)
		}
		stream.ServeRaw(w, r, p.path)
		return
	}
	c, err := r.Cookie(mediaCookie)
	if err != nil {
		http.Error(w, "Nicht angemeldet", http.StatusUnauthorized)
		return
	}
	sess, code := h.S.Auth.Validate(r.Context(), c.Value)
	if code != 0 {
		http.Error(w, "Nicht angemeldet", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("id"))
	backdrop := q.Get("type") == "backdrop"
	var path string
	switch q.Get("kind") {
	case "tvshow":
		if sh, ok := h.lib(sess).Show(id); ok {
			path = sh.PosterPath()
			if backdrop {
				path = ""
				if sh.Meta != nil {
					path = sh.Meta.Backdrop
				}
			}
		}
	default:
		if it, ok := h.lib(sess).Item(id); ok {
			path = map[bool]string{true: it.BackdropPath(), false: it.PosterPath()}[backdrop]
		}
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

type rawToken struct {
	path    string
	account string
	key     string // Verbindung (Sitzung)
	created time.Time
}

// ---------- Mediathek (alle angemeldeten Benutzer) ----------

type libEntry struct {
	ID          int      `json:"id"`
	Kind        string   `json:"kind"`
	Title       string   `json:"title"`
	Year        int      `json:"year,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	Rating      float64  `json:"rating,omitempty"`
	IMDbRating  float64  `json:"imdb_rating,omitempty"`
	MyRating    float64  `json:"my_rating,omitempty"` // eigene Bewertung 1–10
	IMDbID      string   `json:"imdb_id,omitempty"`
	Certificate string   `json:"certificate,omitempty"`
	Runtime     int      `json:"runtime,omitempty"`
	Poster      string   `json:"poster,omitempty"`
	Added       int64    `json:"added"`
	HasMeta     bool     `json:"has_meta"`
	Episodes    int      `json:"episodes,omitempty"`
	Channel     string   `json:"channel,omitempty"`  // TV-Aufnahmen
	Recorded    string   `json:"recorded,omitempty"` // TV-Aufnahmen
	Paths       []string `json:"paths,omitempty"`    // nur für DSM-Administratoren: Datei- bzw. Serienpfade
}

func metaYear(m *library.Meta, fallback int) int {
	if m != nil && len(m.Released) >= 4 {
		if y, err := strconv.Atoi(m.Released[:4]); err == nil {
			return y
		}
	}
	return fallback
}

func imgURL(kind string, id int, typ string, path string) string {
	if path == "" {
		return ""
	}
	v := ""
	if st, err := os.Stat(path); err == nil {
		v = "&v=" + strconv.FormatInt(st.ModTime().Unix(), 10)
	}
	return "/dsvr/media/img?kind=" + kind + "&id=" + strconv.Itoa(id) + "&type=" + typ + v
}

func entryFromMeta(e *libEntry, m *library.Meta) {
	if m == nil || m.NotFound {
		return
	}
	e.HasMeta = true
	if m.Title != "" {
		e.Title = m.Title
	}
	e.Genres, e.Rating, e.IMDbRating, e.IMDbID, e.Certificate, e.Runtime = m.Genres, m.Rating, m.IMDbRating, m.IMDbID, m.Certificate, m.Runtime
}

func (h *Handler) library(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	mine := h.S.Lib.Ratings(sess.Account)
	kind := q.Get("kind")
	// library: Bibliothek (0 = Standardbibliothek des Typs); fehlt = alle.
	var libID *int
	if v := q.Get("library"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			libID = &n
		}
	}
	var out []libEntry
	dsmAdmin := h.S.Auth.IsAdmin(sess)
	switch kind {
	case "tvshow":
		shows, _ := h.lib(sess).Shows(libID, 0, 0)
		eps, _ := h.lib(sess).Items(library.Query{Kind: "episode", LibraryID: libID})
		count := map[int]int{}
		epPaths := map[int][]string{}
		for _, e := range eps {
			count[e.ShowID]++
			if dsmAdmin {
				for _, f := range h.S.Lib.Files(e) {
					epPaths[e.ShowID] = append(epPaths[e.ShowID], f.Path)
				}
			}
		}
		for _, sh := range shows {
			e := libEntry{ID: sh.ID, Kind: "tvshow", Title: sh.Title, Added: sh.AddedAt, Episodes: count[sh.ID], Year: metaYear(sh.Meta, 0)}
			entryFromMeta(&e, sh.Meta)
			e.Title = sh.DisplayTitle()
			e.Poster = imgURL("tvshow", sh.ID, "poster", sh.PosterPath())
			e.MyRating = mine[ratingKey("tvshow", sh.ID)]
			e.Paths = showDirs(epPaths[sh.ID])
			out = append(out, e)
		}
	default:
		if kind == "" {
			kind = "movie"
		}
		items, _ := h.lib(sess).Items(library.Query{Kind: kind, LibraryID: libID})
		for _, it := range items {
			e := libEntry{ID: it.ID, Kind: it.Kind, Title: it.Title, Year: metaYear(it.Meta, it.Year), Added: it.AddedAt,
				Channel: it.Channel, Recorded: it.Recorded}
			entryFromMeta(&e, it.Meta)
			e.Title = it.DisplayTitle()
			e.Poster = imgURL(it.Kind, it.ID, "poster", it.PosterPath())
			e.MyRating = mine[ratingKey(it.Kind, it.ID)]
			if dsmAdmin {
				for _, f := range h.S.Lib.Files(it) {
					e.Paths = append(e.Paths, f.Path)
				}
			}
			out = append(out, e)
		}
	}
	if needle := strings.ToLower(strings.TrimSpace(q.Get("q"))); needle != "" {
		f := out[:0]
		for _, e := range out {
			if strings.Contains(strings.ToLower(e.Title+" "+strings.Join(e.Genres, " ")), needle) {
				f = append(f, e)
			}
		}
		out = f
	}
	switch q.Get("sort") {
	case "added":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Added > out[j].Added })
	case "year":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Year > out[j].Year })
	case "rating":
		sort.SliceStable(out, func(i, j int) bool { return bestRating(out[i]) > bestRating(out[j]) })
	case "my_rating":
		sort.SliceStable(out, func(i, j int) bool { return out[i].MyRating > out[j].MyRating })
	default:
		sort.SliceStable(out, func(i, j int) bool {
			return strings.ToLower(library.SortTitle(out[i].Title)) < strings.ToLower(library.SortTitle(out[j].Title))
		})
	}
	if out == nil {
		out = []libEntry{}
	}
	writeJSON(w, out)
}

// lib: Sicht des Benutzers (Freigaben, Jugendschutz).
func (h *Handler) lib(sess auth.Session) *library.View { return h.S.Lib.For(h.S.AccessFor(sess)) }

func ratingKey(kind string, id int) string { return kind + ":" + strconv.Itoa(id) }

// rate: eigene Bewertung (1–10, halbe Punkte; 0 = entfernen) pro Benutzer.
func (h *Handler) rate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Kind   string  `json:"kind"`
		ID     int     `json:"id"`
		Rating float64 `json:"rating"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch req.Kind {
	case "tvshow":
		if _, ok := h.lib(sess).Show(req.ID); !ok {
			http.NotFound(w, r)
			return
		}
	case "movie", "episode", "home_video":
		if _, ok := h.lib(sess).Item(req.ID); !ok {
			http.NotFound(w, r)
			return
		}
	default:
		http.Error(w, "kind", http.StatusBadRequest)
		return
	}
	v := math.Round(req.Rating*2) / 2
	if v < 0 || v > 10 {
		http.Error(w, "Bewertung 0–10", http.StatusBadRequest)
		return
	}
	if err := h.S.Lib.SetRating(sess.Account, ratingKey(req.Kind, req.ID), v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "my_rating": v})
}

func bestRating(e libEntry) float64 {
	if e.IMDbRating > 0 {
		return e.IMDbRating
	}
	return e.Rating
}

type fileInfo struct {
	ID       int             `json:"id"`
	Name     string          `json:"name"`
	Size     int64           `json:"size"`
	Probe    *library.Probe  `json:"probe,omitempty"`
	Position int             `json:"position"`
	Plan     stream.Decision `json:"plan"`
}

func (h *Handler) fileInfos(it library.Item, account string, caps stream.Caps) []fileInfo {
	var out []fileInfo
	for _, f := range h.S.Lib.Files(it) {
		if f.Probe.NeedsProbe(h.S.FFprobe) && h.S.FFprobe != "" {
			f.Probe = library.RunProbe(h.S.FFprobe, f.Path)
			h.S.Lib.SetProbe(f.ID, f.Probe)
		}
		ws, _ := h.S.Lib.Watch(account, f.ID)
		out = append(out, fileInfo{ID: f.ID, Name: filepath.Base(f.Path), Size: f.Size, Probe: f.Probe, Position: ws.Position,
			Plan: stream.Decide(f.Probe, f.Path, caps)})
	}
	return out
}

func clientCaps(r *http.Request) stream.Caps {
	if r.URL.Query().Get("client") == "apple" {
		return stream.CapsApple
	}
	return stream.CapsBrowser
}

func (h *Handler) item(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("id"))
	caps := clientCaps(r)
	if q.Get("kind") == "tvshow" {
		sh, ok := h.lib(sess).Show(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		eps, _ := h.lib(sess).Items(library.Query{Kind: "episode", ShowID: id})
		type ep struct {
			ID       int        `json:"id"`
			Season   int        `json:"season"`
			Episode  int        `json:"episode"`
			Title    string     `json:"title"`
			Summary  string     `json:"summary,omitempty"`
			Released string     `json:"released,omitempty"`
			Still    string     `json:"still,omitempty"`
			Files    []fileInfo `json:"files"`
		}
		var list []ep
		for _, e := range eps {
			x := ep{ID: e.ID, Season: e.Season, Episode: e.Episode, Title: e.EpisodeTitle(), Files: h.fileInfos(e, sess.Account, caps)}
			if e.Meta != nil && !e.Meta.NotFound {
				x.Summary, x.Released = e.Meta.Summary, e.Meta.Released
				x.Still = imgURL("episode", e.ID, "backdrop", e.Meta.Backdrop)
			}
			list = append(list, x)
		}
		p := sh.PosterPath()
		var bd string
		if sh.Meta != nil {
			bd = sh.Meta.Backdrop
		}
		fsk, fskManual := h.S.Lib.EffectiveFSK("tvshow", sh.ID)
		writeJSON(w, map[string]any{"id": sh.ID, "kind": "tvshow", "title": sh.DisplayTitle(), "custom_title": sh.CustomTitle, "meta": sh.Meta, "fsk": fsk, "fsk_manual": fskManual,
			"my_rating": h.S.Lib.Ratings(sess.Account)[ratingKey("tvshow", sh.ID)],
			"poster":    imgURL("tvshow", sh.ID, "poster", p), "backdrop": imgURL("tvshow", sh.ID, "backdrop", bd), "episodes": list})
		return
	}
	it, ok := h.lib(sess).Item(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	fsk, fskManual := h.S.Lib.EffectiveFSK(it.Kind, it.ID)
	writeJSON(w, map[string]any{"id": it.ID, "kind": it.Kind, "title": it.DisplayTitle(), "custom_title": it.CustomTitle, "year": metaYear(it.Meta, it.Year),
		"channel": it.Channel, "recorded": it.Recorded, "meta": it.Meta, "fsk": fsk, "fsk_manual": fskManual,
		"deletable": h.S.IsAppAdmin(sess) && it.Kind != "tvshow" && len(it.FileIDs) > 0, "recycle": recycleAvailable(h.firstFilePath(it)),
		"my_rating": h.S.Lib.Ratings(sess.Account)[ratingKey(it.Kind, it.ID)],
		"poster":    imgURL(it.Kind, it.ID, "poster", it.PosterPath()), "backdrop": imgURL(it.Kind, it.ID, "backdrop", it.BackdropPath()),
		"files": h.fileInfos(it, sess.Account, caps)})
}

// play startet die Wiedergabe im Browser: direkt (Range) oder HLS (FFmpeg).
func (h *Handler) play(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.Method == http.MethodDelete {
		tok := r.URL.Query().Get("token")
		h.mu.Lock()
		delete(h.raw, tok)
		h.mu.Unlock()
		if h.S.HLS != nil {
			if j, ok := h.S.HLS.Get(tok); ok && j.Account == sess.Account {
				h.S.HLS.Stop(tok)
			}
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	var req struct {
		FileID int     `json:"file_id"`
		Client string  `json:"client"`
		Start  float64 `json:"start"`
		Force  string  `json:"force"` // "transcode" erzwingt H.264
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f, ok := h.lib(sess).File(req.FileID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	caps := stream.CapsBrowser
	if req.Client == "apple" {
		caps = stream.CapsApple
	}
	if f.Probe.NeedsProbe(h.S.FFprobe) {
		f.Probe = library.RunProbe(h.S.FFprobe, f.Path)
		h.S.Lib.SetProbe(f.ID, f.Probe)
	}
	d := stream.Decide(f.Probe, f.Path, caps)
	if req.Force == "transcode" {
		d = stream.Decision{Mode: stream.ModeTranscode, Reason: "manuell erzwungen"}
	}
	if d.Mode == stream.ModeDirect {
		tok := stream.NewStreamID()
		h.mu.Lock()
		h.raw[tok] = rawToken{path: f.Path, account: sess.Account, key: sess.Key, created: time.Now()}
		h.mu.Unlock()
		h.S.Conns().Play(sess.Key, tok, h.playTitle(sess, f))
		writeJSON(w, map[string]any{"mode": d.Mode, "reason": d.Reason, "token": tok,
			"url": "/dsvr/media/raw/" + tok + "/" + filepath.Base(f.Path), "start": req.Start})
		return
	}
	if h.S.HLS == nil {
		http.Error(w, stream.ErrNoFFmpeg.Error(), http.StatusServiceUnavailable)
		return
	}
	if f.Probe != nil && f.Probe.Duration > 0 {
		d.Duration = f.Probe.Duration
	}
	j, err := h.S.HLS.Start(sess.Account, f.Path, d, req.Start, 0)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	h.S.Conns().Play(sess.Key, j.ID, h.playTitle(sess, f))
	if j.VOD { // ganze Laufzeit in der Liste: Player springt selbst zur Startstelle
		writeJSON(w, map[string]any{"mode": d.Mode, "reason": d.Reason, "token": j.ID, "url": "/hls/" + j.ID + "/index.m3u8", "offset": 0, "seek": req.Start})
		return
	}
	writeJSON(w, map[string]any{"mode": d.Mode, "reason": d.Reason, "token": j.ID, "url": "/hls/" + j.ID + "/index.m3u8", "offset": req.Start})
}

// playTitle: Anzeigename für die Verbindungsliste.
func (h *Handler) playTitle(sess auth.Session, f library.File) string {
	if it, ok := h.lib(sess).Item(f.ItemID); ok {
		if it.Kind == "episode" {
			return fmt.Sprintf("%s S%02dE%02d", it.Show, it.Season, it.Episode)
		}
		return it.DisplayTitle()
	}
	return filepath.Base(f.Path)
}

func (h *Handler) progress(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var req struct {
		FileID   int `json:"file_id"`
		Position int `json:"position"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_ = h.S.Lib.SetWatch(sess.Account, req.FileID, req.Position)
	writeJSON(w, map[string]any{"ok": true})
}

func (h *Handler) gcRaw() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.raw {
		if time.Since(v.created) > 12*time.Hour {
			delete(h.raw, k)
		}
	}
}

// ---------- Metadaten (Admin) ----------

func (h *Handler) metaAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"` // refresh | assign
		Kind   string `json:"kind"`
		ID     int    `json:"id"`
		Ref    string `json:"ref"`     // tt…, Q… oder TMDb-Nummer
		TMDbID int    `json:"tmdb_id"` // ältere Oberfläche
	}
	if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "refresh":
		h.S.Lib.ClearMeta(req.ID)
		h.S.Meta.Trigger()
		writeJSON(w, map[string]any{"ok": true, "message": "Metadaten werden neu geladen"})
	case "assign":
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		ref := strings.TrimSpace(req.Ref)
		if ref == "" && req.TMDbID > 0 {
			ref = strconv.Itoa(req.TMDbID)
		}
		m, err := h.S.Meta.Assign(ctx, req.Kind, req.ID, ref)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "title": m.Title})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

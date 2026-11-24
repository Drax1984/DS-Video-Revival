package jellyfin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"dsvideorevival/internal/library"
)

// ---------- Bibliotheken (Ansichten) ----------

type viewInfo struct {
	Typ  string // movie | tvshow | home_video | tv_record
	Lib  int
	Name string
}

func (v viewInfo) id() string { return viewID(v.Typ, v.Lib) }

func collectionType(typ string) string {
	switch typ {
	case "movie":
		return "movies"
	case "tvshow":
		return "tvshows"
	}
	return "homevideos"
}

// german: Namen der Standardbibliotheken nach der Metadaten-Sprache.
func (h *Handler) german() bool {
	return strings.HasPrefix(strings.ToLower(h.S.Cfg().MetaLanguage), "de")
}

func (h *Handler) defaultViewName(typ string) string {
	de := map[string]string{"movie": "Filme", "tvshow": "Serien", "home_video": "Heimvideos", "tv_record": "TV-Aufnahmen"}
	en := map[string]string{"movie": "Movies", "tvshow": "TV Shows", "home_video": "Home Videos", "tv_record": "TV Recordings"}
	if h.german() {
		return de[typ]
	}
	return en[typ]
}

// allViews: Standard- und eigene Bibliotheken laut Konfiguration.
func (h *Handler) allViews() []viewInfo {
	cfg := h.S.Cfg()
	var out []viewInfo
	for _, t := range viewTypes[1:] {
		for _, f := range cfg.Folders {
			if f.Type == t && f.LibraryID == 0 {
				out = append(out, viewInfo{Typ: t, Name: h.defaultViewName(t)})
				break
			}
		}
	}
	for _, l := range cfg.Libraries {
		out = append(out, viewInfo{Typ: l.Type, Lib: l.ID, Name: l.Title})
	}
	return out
}

// views: Bibliotheken, in denen der Benutzer etwas sehen darf.
func (h *Handler) views(x *req) []viewInfo {
	var out []viewInfo
	for _, v := range h.allViews() {
		if h.viewCount(x, v) > 0 {
			out = append(out, v)
		}
	}
	return out
}

func (h *Handler) viewCount(x *req, v viewInfo) int {
	lib := v.Lib
	if v.Typ == "tvshow" {
		_, n := x.view.ShowsQ(library.ShowQuery{LibraryID: &lib, Limit: 1})
		return n
	}
	_, n := x.view.Items(library.Query{Kind: v.Typ, LibraryID: &lib, Limit: 1})
	return n
}

func (h *Handler) findView(id string) (viewInfo, bool) {
	k, p, ok := parseID(id)
	if !ok || k != kView {
		return viewInfo{}, false
	}
	typ, lib, ok := parseViewPayload(p)
	if !ok {
		return viewInfo{}, false
	}
	for _, v := range h.allViews() {
		if v.Typ == typ && v.Lib == lib {
			return v, true
		}
	}
	return viewInfo{}, false
}

// ---------- Einträge ----------

// entry: ein Objekt einer Liste (Film/Folge/Video, Serie, Staffel, Bibliothek).
type entry struct {
	kind   byte
	it     library.Item
	sh     library.Show
	season int // kSeason
	view   viewInfo
}

func (e entry) id() string {
	switch e.kind {
	case kItem:
		return makeID(kItem, uint64(e.it.ID))
	case kShow:
		return makeID(kShow, uint64(e.sh.ID))
	case kSeason:
		return seasonID(e.sh.ID, e.season)
	case kView:
		return e.view.id()
	}
	return makeID(kRoot, 0)
}

// favKey: Schlüssel für Favoriten.
func (e entry) favKey() string {
	switch e.kind {
	case kItem:
		return "item:" + strconv.Itoa(e.it.ID)
	case kShow:
		return "show:" + strconv.Itoa(e.sh.ID)
	case kSeason:
		return fmt.Sprintf("season:%d:%d", e.sh.ID, e.season)
	}
	return "view:" + e.view.id()
}

func (e entry) isFolder() bool { return e.kind != kItem }

func (e entry) name(h *Handler) string {
	switch e.kind {
	case kItem:
		if e.it.Kind == "episode" {
			if t := strings.TrimSpace(e.it.EpisodeTitle()); t != "" {
				return t
			}
			if h.german() { // Folge ohne Titel (nur „S01E02“ im Dateinamen)
				return "Folge " + strconv.Itoa(e.it.Episode)
			}
			return "Episode " + strconv.Itoa(e.it.Episode)
		}
		return e.it.DisplayTitle()
	case kShow:
		return e.sh.DisplayTitle()
	case kSeason:
		return h.seasonName(e.season)
	case kView:
		return e.view.Name
	}
	return ""
}

// sortName: Jellyfin sortiert Folgen und Staffeln über Nummern im Sortiernamen.
func (e entry) sortName(h *Handler) string {
	switch e.kind {
	case kItem:
		if e.it.Kind == "episode" {
			return fmt.Sprintf("%04d - %04d - %s", e.it.Season, e.it.Episode, strings.ToLower(e.it.EpisodeTitle()))
		}
		if e.it.SortTitle != "" {
			return strings.ToLower(e.it.SortTitle)
		}
	case kShow:
		if e.sh.SortTitle != "" {
			return strings.ToLower(e.sh.SortTitle)
		}
	case kSeason:
		return fmt.Sprintf("%04d", e.season)
	}
	return strings.ToLower(e.name(h))
}

func (e entry) meta() *library.Meta {
	switch e.kind {
	case kItem:
		return e.it.Meta
	case kShow, kSeason:
		return e.sh.Meta
	}
	return nil
}

func (e entry) typeName() string {
	switch e.kind {
	case kItem:
		switch e.it.Kind {
		case "movie":
			return "Movie"
		case "episode":
			return "Episode"
		}
		return "Video"
	case kShow:
		return "Series"
	case kSeason:
		return "Season"
	case kView:
		return "CollectionFolder"
	}
	return "UserRootFolder"
}

func (e entry) year() int {
	m := e.meta()
	if m != nil && !m.NotFound && len(m.Released) >= 4 {
		if y, err := strconv.Atoi(m.Released[:4]); err == nil {
			return y
		}
	}
	if e.kind == kItem {
		return e.it.Year
	}
	return 0
}

func (e entry) released() string {
	if m := e.meta(); m != nil && !m.NotFound && e.kind != kSeason {
		return m.Released
	}
	return ""
}

func (e entry) rating() float64 {
	m := e.meta()
	if m == nil || m.NotFound || e.kind == kSeason {
		return 0
	}
	if m.IMDbRating > 0 {
		return m.IMDbRating
	}
	return m.Rating
}

func (e entry) genres() []string {
	if m := e.meta(); m != nil && !m.NotFound && e.kind != kSeason && !(e.kind == kItem && e.it.Kind == "episode") {
		return m.Genres
	}
	return nil
}

func (h *Handler) seasonName(n int) string {
	if n == 0 {
		if h.german() {
			return "Extras"
		}
		return "Specials"
	}
	if h.german() {
		return "Staffel " + strconv.Itoa(n)
	}
	return "Season " + strconv.Itoa(n)
}

// resolve: Objekt zu einer ID (mit Zugriffsprüfung).
func (h *Handler) resolve(x *req, id string) (entry, bool) {
	k, p, ok := parseID(id)
	if !ok {
		return entry{}, false
	}
	switch k {
	case kItem:
		it, ok := x.view.Item(int(p))
		return entry{kind: kItem, it: it}, ok
	case kShow:
		sh, ok := x.view.Show(int(p))
		return entry{kind: kShow, sh: sh}, ok
	case kSeason:
		sh, ok := x.view.Show(int(p >> 16))
		if !ok {
			return entry{}, false
		}
		n := int(uint16(p))
		for _, s := range h.seasons(x, sh.ID) {
			if s == n {
				return entry{kind: kSeason, sh: sh, season: n}, true
			}
		}
		return entry{}, false
	case kView:
		v, ok := h.findView(id)
		if !ok || h.viewCount(x, v) == 0 {
			return entry{}, false
		}
		return entry{kind: kView, view: v}, true
	case kFile:
		f, ok := x.view.File(int(p))
		if !ok {
			return entry{}, false
		}
		it, ok := x.view.Item(f.ItemID)
		return entry{kind: kItem, it: it}, ok
	case kRoot:
		return entry{kind: kRoot}, true
	}
	return entry{}, false
}

// episodes: alle sichtbaren Folgen je Serie (einmal je Anfrage).
func (h *Handler) episodes(x *req) map[int][]library.Item {
	if x.eps == nil {
		items, _ := x.view.Items(library.Query{Kind: "episode"})
		x.eps = map[int][]library.Item{}
		for _, it := range items {
			x.eps[it.ShowID] = append(x.eps[it.ShowID], it)
		}
	}
	return x.eps
}

func (h *Handler) seasons(x *req, showID int) []int {
	seen := map[int]bool{}
	var out []int
	for _, e := range h.episodes(x)[showID] {
		if !seen[e.Season] {
			seen[e.Season] = true
			out = append(out, e.Season)
		}
	}
	sort.Ints(out)
	return out
}

// ---------- Wiedergabestand ----------

type state struct {
	Pos    float64 // Sekunden (zuletzt genutzte Version)
	Dur    float64
	Last   int64
	Played bool
	FileID int
}

func (s state) resumable() bool { return !s.Played && s.Pos >= 30 }

func fileDuration(f library.File, it library.Item) float64 {
	if f.Probe != nil && f.Probe.Duration > 0 {
		return f.Probe.Duration
	}
	if it.Meta != nil && it.Meta.Runtime > 0 {
		return float64(it.Meta.Runtime * 60)
	}
	return 0
}

// isPlayed: wie Jellyfin (90 %) bzw. die DS-video-Apps (letzte 3 Minuten).
func isPlayed(pos, dur float64) bool {
	return dur > 0 && (pos >= 0.9*dur || (dur > 600 && pos >= dur-180))
}

func (h *Handler) state(x *req, it library.Item) state {
	if s, ok := x.states[it.ID]; ok {
		return s
	}
	var st state
	for _, f := range h.S.Lib.Files(it) {
		dur := fileDuration(f, it)
		if st.Dur == 0 {
			st.Dur = dur
		}
		ws, ok := h.S.Lib.Watch(x.sess.Account, f.ID)
		if !ok || ws.UpdatedAt < st.Last {
			continue
		}
		st.Pos, st.Last, st.FileID = float64(ws.Position), ws.UpdatedAt, f.ID
		if dur > 0 {
			st.Dur = dur
		}
	}
	st.Played = st.Last > 0 && isPlayed(st.Pos, st.Dur)
	if x.states == nil {
		x.states = map[int]state{}
	}
	x.states[it.ID] = st
	return st
}

// folderState: Folgen gesamt/gesehen und zuletzt gesehen (Serie oder Staffel).
func (h *Handler) folderState(x *req, e entry) (total, played int, last int64) {
	for _, ep := range h.episodes(x)[e.sh.ID] {
		if e.kind == kSeason && ep.Season != e.season {
			continue
		}
		total++
		st := h.state(x, ep)
		if st.Played {
			played++
		}
		if st.Last > last {
			last = st.Last
		}
	}
	return
}

func (h *Handler) lastPlayed(x *req, e entry) int64 {
	switch e.kind {
	case kItem:
		return h.state(x, e.it).Last
	case kShow, kSeason:
		_, _, last := h.folderState(x, e)
		return last
	}
	return 0
}

func (h *Handler) userData(x *req, e entry) map[string]any {
	id := e.id()
	ud := map[string]any{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": h.store.favorite(x.sess.Account, e.favKey()),
		"Played": false, "Key": id, "ItemId": id}
	switch e.kind {
	case kItem:
		st := h.state(x, e.it)
		if st.resumable() {
			ud["PlaybackPositionTicks"] = ticks(st.Pos)
			if st.Dur > 0 {
				ud["PlayedPercentage"] = math.Round(st.Pos/st.Dur*1000) / 10
			}
		}
		if st.Played {
			ud["Played"], ud["PlayCount"] = true, 1
		}
		if st.Last > 0 {
			ud["LastPlayedDate"] = unixTime(st.Last)
		}
	case kShow, kSeason:
		total, played, last := h.folderState(x, e)
		ud["UnplayedItemCount"] = total - played
		if total > 0 {
			ud["PlayedPercentage"] = math.Round(float64(played)/float64(total)*1000) / 10
			if played == total {
				ud["Played"] = true
			}
		}
		if last > 0 {
			ud["LastPlayedDate"] = unixTime(last)
		}
	}
	return ud
}

// ---------- Bilder ----------

// imagePath: Bilddatei eines Objekts (Primary, Backdrop, Thumb).
func (h *Handler) imagePath(e entry, typ string) string {
	typ = strings.ToLower(typ)
	valid := func(p string) string {
		if p != "" && library.ValidImage(p) {
			return p
		}
		return ""
	}
	switch e.kind {
	case kItem:
		it := e.it
		if it.Kind == "episode" {
			if typ != "primary" && typ != "thumb" {
				return ""
			}
			if p := valid(it.Poster); p != "" {
				return p
			}
			if it.Meta != nil {
				if p := valid(it.Meta.Backdrop); p != "" { // Szenenbild der Folge
					return p
				}
				return valid(it.Meta.Poster)
			}
			return ""
		}
		switch typ {
		case "primary":
			return it.PosterPath()
		case "backdrop", "thumb":
			return valid(it.BackdropPath())
		}
	case kShow, kSeason:
		switch typ {
		case "primary":
			return e.sh.PosterPath()
		case "backdrop", "thumb":
			if e.sh.Meta != nil {
				return valid(e.sh.Meta.Backdrop)
			}
		}
	}
	return ""
}

// imageTag: fälschungssicherer Bild-Tag (HMAC über Pfad und Änderungszeit).
// Bilder dürfen die Apps wie bei Jellyfin ohne Anmeldung laden – aber nur
// mit gültigem Tag aus einer angemeldeten Abfrage.
func (h *Handler) imageTag(id, typ, path string) string {
	if path == "" {
		return ""
	}
	var mt int64
	if st, err := os.Stat(path); err == nil {
		mt = st.ModTime().UnixNano()
	}
	m := hmac.New(sha256.New, h.store.secret())
	fmt.Fprintf(m, "%s|%s|%s|%d", normID(id), strings.ToLower(typ), path, mt)
	return hex.EncodeToString(m.Sum(nil))[:20]
}

func (h *Handler) image(x *req) {
	id, typ := x.raw[1], x.segs[3]
	k, p, ok := parseID(id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	var e entry
	switch k {
	case kItem:
		it, ok := h.S.Lib.Item(int(p))
		if !ok {
			http.NotFound(x.w, x.r)
			return
		}
		e = entry{kind: kItem, it: it}
	case kShow:
		sh, ok := h.S.Lib.Show(int(p))
		if !ok {
			http.NotFound(x.w, x.r)
			return
		}
		e = entry{kind: kShow, sh: sh}
	case kSeason:
		sh, ok := h.S.Lib.Show(int(p >> 16))
		if !ok {
			http.NotFound(x.w, x.r)
			return
		}
		e = entry{kind: kSeason, sh: sh, season: int(uint16(p))}
	default:
		http.NotFound(x.w, x.r)
		return
	}
	path := h.imagePath(e, typ)
	if path == "" {
		http.NotFound(x.w, x.r)
		return
	}
	tag := x.Q("tag")
	authorized := tag != "" && hmac.Equal([]byte(tag), []byte(h.imageTag(id, typ, path)))
	if !authorized {
		// ohne gültigen Tag nur angemeldet und mit Zugriff auf das Objekt
		dev, ok := h.store.lookup(x.auth.Token, "")
		if !ok {
			http.Error(x.w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		x.dev = dev
		x.sess.Account, x.sess.IsManager, x.sess.Key = dev.Account, dev.IsManager, dev.key()
		x.view = h.S.Lib.For(h.S.AccessFor(x.sess))
		if _, ok := h.resolve(x, id); !ok {
			http.NotFound(x.w, x.r)
			return
		}
	}
	if authorized {
		x.w.Header().Set("Cache-Control", "public, max-age=31536000")
	} else {
		x.w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	http.ServeFile(x.w, x.r, path)
}

// ---------- Objekt → BaseItemDto ----------

type dtoOpts struct {
	full   bool // Einzelabruf: alle Felder, Versionen und Spuren
	fields map[string]bool
}

func (o dtoOpts) has(f string) bool { return o.full || o.fields[strings.ToLower(f)] }

func (x *req) dtoOpts() dtoOpts {
	o := dtoOpts{fields: map[string]bool{}}
	for _, f := range x.QList("Fields") {
		o.fields[strings.ToLower(f)] = true
	}
	return o
}

func parseDate(s string) (time.Time, bool) {
	if len(s) < 10 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s[:10])
	return t, err == nil
}

func (h *Handler) parentID(e entry) string {
	switch e.kind {
	case kItem:
		if e.it.Kind == "episode" {
			return seasonID(e.it.ShowID, e.it.Season)
		}
		return viewID(e.it.Kind, e.it.LibraryID)
	case kShow:
		return viewID("tvshow", e.sh.LibraryID)
	case kSeason:
		return makeID(kShow, uint64(e.sh.ID))
	case kView:
		return makeID(kRoot, 0)
	}
	return ""
}

func (h *Handler) dto(x *req, e entry, o dtoOpts) map[string]any {
	id := e.id()
	d := map[string]any{
		"Name": e.name(h), "ServerId": h.store.serverID(), "Id": id, "Type": e.typeName(), "IsFolder": e.isFolder(),
		"SortName": e.sortName(h), "LocationType": "FileSystem", "CanDelete": false, "Etag": id[24:],
	}
	if p := h.parentID(e); p != "" {
		d["ParentId"] = p
	}
	if e.kind == kView {
		d["CollectionType"] = collectionType(e.view.Typ)
		d["ChildCount"] = h.viewCount(x, e.view)
		d["ImageTags"] = map[string]string{}
		d["BackdropImageTags"] = []string{}
		d["UserData"] = h.userData(x, e)
		d["PrimaryImageAspectRatio"] = 1.7777777777777777
		return d
	}
	if e.kind == kRoot {
		d["Name"] = "Media Folders"
		d["ImageTags"] = map[string]string{}
		d["BackdropImageTags"] = []string{}
		return d
	}
	m := e.meta()
	if m != nil && m.NotFound {
		m = nil
	}
	// Bilder
	tags := map[string]string{}
	if p := h.imagePath(e, "Primary"); p != "" {
		tags["Primary"] = h.imageTag(id, "primary", p)
	}
	backdrops := []string{}
	if p := h.imagePath(e, "Backdrop"); p != "" && !(e.kind == kItem && e.it.Kind == "episode") {
		backdrops = append(backdrops, h.imageTag(id, "backdrop", p))
		tags["Thumb"] = h.imageTag(id, "thumb", p)
	}
	d["ImageTags"], d["BackdropImageTags"] = tags, backdrops
	// Texte und Angaben
	if y := e.year(); y > 0 {
		d["ProductionYear"] = y
	}
	if rel := e.released(); rel != "" {
		if t, ok := parseDate(rel); ok {
			d["PremiereDate"] = jfTime(t)
		}
	}
	if r := e.rating(); r > 0 {
		d["CommunityRating"] = math.Round(r*10) / 10
	}
	g := e.genres()
	if g == nil {
		g = []string{}
	}
	d["Genres"] = g
	gi := []map[string]string{}
	for _, n := range g {
		gi = append(gi, map[string]string{"Name": n, "Id": nameID(kGenre, n)})
	}
	d["GenreItems"] = gi
	if m != nil && e.kind != kSeason {
		if m.Summary != "" {
			d["Overview"] = m.Summary
		}
		if m.Tagline != "" {
			d["Taglines"] = []string{m.Tagline}
		}
		if m.OriginalTitle != "" {
			d["OriginalTitle"] = m.OriginalTitle
		}
		prov := map[string]string{}
		if m.IMDbID != "" {
			prov["Imdb"] = m.IMDbID
		}
		if m.TMDbID > 0 {
			prov["Tmdb"] = strconv.Itoa(m.TMDbID)
		}
		d["ProviderIds"] = prov
		if o.has("People") {
			d["People"] = people(m)
		}
	}
	if _, ok := d["ProviderIds"]; !ok {
		d["ProviderIds"] = map[string]string{}
	}
	if fsk := h.officialRating(e); fsk != "" {
		d["OfficialRating"] = fsk
	}
	d["UserData"] = h.userData(x, e)
	switch e.kind {
	case kItem:
		h.itemFields(x, e, d, o)
	case kShow:
		d["DateCreated"] = unixTime(e.sh.AddedAt)
		d["PrimaryImageAspectRatio"] = 2.0 / 3
		seasons := h.seasons(x, e.sh.ID)
		d["ChildCount"] = len(seasons)
		d["RecursiveItemCount"] = len(h.episodes(x)[e.sh.ID])
		d["Status"] = "Continuing"
		d["AirDays"] = []string{}
	case kSeason:
		d["IndexNumber"] = e.season
		d["SeriesId"] = makeID(kShow, uint64(e.sh.ID))
		d["SeriesName"] = e.sh.DisplayTitle()
		d["PrimaryImageAspectRatio"] = 2.0 / 3
		n := 0
		for _, ep := range h.episodes(x)[e.sh.ID] {
			if ep.Season == e.season {
				n++
			}
		}
		d["ChildCount"] = n
		h.seriesImages(e.sh, d)
	}
	return d
}

// seriesImages: Bilder der Serie für Staffeln und Folgen (Rückfall der Apps).
func (h *Handler) seriesImages(sh library.Show, d map[string]any) {
	sid := makeID(kShow, uint64(sh.ID))
	se := entry{kind: kShow, sh: sh}
	if p := h.imagePath(se, "Primary"); p != "" {
		d["SeriesPrimaryImageTag"] = h.imageTag(sid, "primary", p)
	}
	if p := h.imagePath(se, "Backdrop"); p != "" {
		d["ParentBackdropItemId"] = sid
		d["ParentBackdropImageTags"] = []string{h.imageTag(sid, "backdrop", p)}
		d["ParentThumbItemId"] = sid
		d["ParentThumbImageTag"] = h.imageTag(sid, "thumb", p)
	}
}

func (h *Handler) officialRating(e entry) string {
	if m := e.meta(); m != nil && !m.NotFound && m.Certificate != "" && e.kind != kSeason {
		if e.kind == kItem && e.it.FSK == nil || e.kind == kShow && e.sh.FSK == nil {
			return m.Certificate
		}
	}
	var fsk int
	switch e.kind {
	case kItem:
		fsk, _ = h.S.Lib.EffectiveFSK(e.it.Kind, e.it.ID)
	case kShow:
		fsk, _ = h.S.Lib.EffectiveFSK("tvshow", e.sh.ID)
	default:
		return ""
	}
	if fsk < 0 {
		return ""
	}
	return "FSK " + strconv.Itoa(fsk)
}

func people(m *library.Meta) []map[string]any {
	out := []map[string]any{}
	add := func(names []string, typ string, max int) {
		for i, n := range names {
			if i >= max {
				break
			}
			out = append(out, map[string]any{"Name": n, "Id": nameID(kPerson, n), "Type": typ})
		}
	}
	add(m.Directors, "Director", 5)
	add(m.Writers, "Writer", 5)
	add(m.Actors, "Actor", 30)
	return out
}

// itemFields: Film, Folge, Heimvideo, TV-Aufnahme.
func (h *Handler) itemFields(x *req, e entry, d map[string]any, o dtoOpts) {
	it := e.it
	d["DateCreated"] = unixTime(it.AddedAt)
	d["MediaType"] = "Video"
	d["VideoType"] = "VideoFile"
	d["CanDownload"] = h.S.MayDownload(x.sess)
	d["PrimaryImageAspectRatio"] = 2.0 / 3
	files := h.S.Lib.Files(it)
	d["MediaSourceCount"] = len(files)
	if len(files) > 0 {
		f := files[0]
		if dur := fileDuration(f, it); dur > 0 {
			d["RunTimeTicks"] = ticks(dur)
		}
		d["Container"] = container(f.Path)
		if o.has("Path") {
			// Nur der Dateiname, keine Ordner der NAS. Die App „Jellyfin“ für
			// Android benennt damit die heruntergeladene Datei und bricht ohne
			// „Path“ das Herunterladen ab („Missing item path“).
			d["Path"] = filepath.Base(f.Path)
		}
		if f.Probe != nil && f.Probe.OK {
			if f.Probe.Width > 0 {
				d["Width"], d["Height"] = f.Probe.Width, f.Probe.Height
			}
		}
		d["HasSubtitles"] = len(vsSubtitles(f)) > 0 || (f.Probe != nil && len(f.Probe.Subtitles) > 0)
	}
	if it.Kind == "episode" {
		d["PrimaryImageAspectRatio"] = 16.0 / 9
		d["IndexNumber"] = it.Episode
		if it.EpisodeEnd > it.Episode {
			d["IndexNumberEnd"] = it.EpisodeEnd
		}
		d["ParentIndexNumber"] = it.Season
		d["SeriesId"] = makeID(kShow, uint64(it.ShowID))
		d["SeasonId"] = seasonID(it.ShowID, it.Season)
		d["SeasonName"] = h.seasonName(it.Season)
		if sh, ok := h.S.Lib.Show(it.ShowID); ok {
			d["SeriesName"] = sh.DisplayTitle()
			h.seriesImages(sh, d)
		} else {
			d["SeriesName"] = it.Show
		}
	}
	if it.Kind == "home_video" || it.Kind == "tv_record" {
		d["PrimaryImageAspectRatio"] = 16.0 / 9
	}
	if o.has("MediaSources") || o.has("MediaStreams") {
		srcs := h.mediaSources(x, it, files, nil)
		if o.has("MediaSources") {
			d["MediaSources"] = srcs
		}
		if len(srcs) > 0 {
			d["MediaStreams"] = srcs[0]["MediaStreams"]
		}
	}
	if o.has("Chapters") {
		d["Chapters"] = []any{}
	}
}

func container(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	switch ext {
	case "m4v":
		return "mp4"
	case "m2ts", "mts":
		return "ts"
	case "mpg", "vob":
		return "mpeg"
	case "wmv":
		return "asf"
	}
	return ext
}

// ---------- Abfragen ----------

func lowerSet(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[strings.ToLower(s)] = true
	}
	return m
}

// typeOf: Jellyfin-Typname eines Eintrags (klein).
func (e entry) typeKey() string { return strings.ToLower(e.typeName()) }

func (h *Handler) allOfLib(x *req, typ string, lib *int) []entry {
	var out []entry
	if typ == "tvshow" {
		shows, _ := x.view.ShowsQ(library.ShowQuery{LibraryID: lib})
		for _, s := range shows {
			out = append(out, entry{kind: kShow, sh: s})
		}
		return out
	}
	items, _ := x.view.Items(library.Query{Kind: typ, LibraryID: lib})
	for _, it := range items {
		out = append(out, entry{kind: kItem, it: it})
	}
	return out
}

func (h *Handler) episodeEntries(x *req, showID int, season *int, lib *int) []entry {
	var out []entry
	add := func(eps []library.Item) {
		for _, ep := range eps {
			if season != nil && ep.Season != *season {
				continue
			}
			if lib != nil && ep.LibraryID != *lib {
				continue
			}
			out = append(out, entry{kind: kItem, it: ep})
		}
	}
	if showID > 0 {
		add(h.episodes(x)[showID])
		return out
	}
	for _, eps := range h.episodes(x) {
		add(eps)
	}
	return out
}

func (h *Handler) seasonEntries(x *req, sh library.Show) []entry {
	var out []entry
	for _, n := range h.seasons(x, sh.ID) {
		out = append(out, entry{kind: kSeason, sh: sh, season: n})
	}
	return out
}

func (h *Handler) allSeasons(x *req, lib *int) []entry {
	var out []entry
	shows, _ := x.view.ShowsQ(library.ShowQuery{LibraryID: lib})
	for _, s := range shows {
		out = append(out, h.seasonEntries(x, s)...)
	}
	return out
}

// collect: Kandidaten unterhalb von parent (vor Filtern und Sortierung).
func (h *Handler) collect(x *req, parent string, types map[string]bool, recursive bool) []entry {
	if parent != "" {
		pk, pp, ok := parseID(parent)
		if !ok {
			return nil
		}
		switch pk {
		case kView:
			v, ok := h.findView(parent)
			if !ok {
				return nil
			}
			lib := v.Lib
			if v.Typ == "tvshow" {
				var out []entry
				if types["episode"] {
					out = append(out, h.episodeEntries(x, 0, nil, &lib)...)
				}
				if types["season"] {
					out = append(out, h.allSeasons(x, &lib)...)
				}
				if len(types) == 0 || types["series"] {
					out = append(out, h.allOfLib(x, "tvshow", &lib)...)
				}
				return out
			}
			if len(types) == 0 || types["movie"] || types["video"] || types["episode"] {
				return h.allOfLib(x, v.Typ, &lib)
			}
			return nil
		case kShow:
			sh, ok := x.view.Show(int(pp))
			if !ok {
				return nil
			}
			if types["episode"] || (recursive && len(types) == 0) {
				return h.episodeEntries(x, sh.ID, nil, nil)
			}
			return h.seasonEntries(x, sh)
		case kSeason:
			sh, ok := x.view.Show(int(pp >> 16))
			if !ok {
				return nil
			}
			n := int(uint16(pp))
			return h.episodeEntries(x, sh.ID, &n, nil)
		case kRoot:
			parent = ""
		default:
			return nil
		}
	}
	// ohne übergeordnetes Objekt: alle Bibliotheken
	var out []entry
	if len(types) == 0 {
		if !recursive {
			for _, v := range h.views(x) {
				out = append(out, entry{kind: kView, view: v})
			}
			return out
		}
		types = map[string]bool{"movie": true, "series": true, "video": true}
	}
	if types["movie"] {
		out = append(out, h.allOfLib(x, "movie", nil)...)
	}
	if types["series"] {
		out = append(out, h.allOfLib(x, "tvshow", nil)...)
	}
	if types["season"] {
		out = append(out, h.allSeasons(x, nil)...)
	}
	if types["episode"] {
		out = append(out, h.episodeEntries(x, 0, nil, nil)...)
	}
	if types["video"] {
		out = append(out, h.allOfLib(x, "home_video", nil)...)
		out = append(out, h.allOfLib(x, "tv_record", nil)...)
	}
	return out
}

// filter wendet die üblichen Filter der Apps an.
func (h *Handler) filter(x *req, es []entry, types, excl map[string]bool) []entry {
	term := strings.ToLower(strings.TrimSpace(x.Q("SearchTerm")))
	flt := lowerSet(x.QList("Filters"))
	if v, ok := x.QBool("IsPlayed"); ok {
		if v {
			flt["isplayed"] = true
		} else {
			flt["isunplayed"] = true
		}
	}
	if v, ok := x.QBool("IsFavorite"); ok && v {
		flt["isfavorite"] = true
	}
	genres := lowerSet(x.QList("Genres"))
	genreIDs := map[string]bool{}
	for _, g := range x.QList("GenreIds") {
		genreIDs[normID(g)] = true
	}
	personIDs := map[string]bool{}
	for _, p := range x.QList("PersonIds") {
		personIDs[normID(p)] = true
	}
	years := map[int]bool{}
	for _, y := range x.QList("Years") {
		if n, err := strconv.Atoi(y); err == nil {
			years[n] = true
		}
	}
	mediaTypes := lowerSet(x.QList("MediaTypes"))
	startsWith := strings.ToLower(x.Q("NameStartsWith"))
	startsOrGreater := strings.ToLower(x.Q("NameStartsWithOrGreater"))
	lessThan := strings.ToLower(x.Q("NameLessThan"))
	out := es[:0:0]
	for _, e := range es {
		tk := e.typeKey()
		if len(types) > 0 && e.kind != kView && !types[tk] {
			continue
		}
		if excl[tk] {
			continue
		}
		if len(mediaTypes) > 0 && (!mediaTypes["video"] || e.kind != kItem) {
			continue
		}
		name := strings.ToLower(e.name(h))
		if term != "" {
			hay := name
			if e.kind == kItem {
				hay += " " + strings.ToLower(e.it.Title)
			}
			if m := e.meta(); m != nil && m.OriginalTitle != "" {
				hay += " " + strings.ToLower(m.OriginalTitle)
			}
			if !strings.Contains(hay, term) {
				continue
			}
		}
		sn := e.sortName(h)
		if startsWith != "" && !strings.HasPrefix(sn, startsWith) {
			continue
		}
		if startsOrGreater != "" && sn < startsOrGreater {
			continue
		}
		if lessThan != "" && sn >= lessThan {
			continue
		}
		if len(years) > 0 && !years[e.year()] {
			continue
		}
		if len(genres) > 0 || len(genreIDs) > 0 {
			hit := false
			for _, g := range e.genres() {
				if genres[strings.ToLower(g)] || genreIDs[nameID(kGenre, g)] {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		if len(personIDs) > 0 {
			hit := false
			if m := e.meta(); m != nil {
				for _, l := range [][]string{m.Actors, m.Directors, m.Writers} {
					for _, n := range l {
						if personIDs[nameID(kPerson, n)] {
							hit = true
						}
					}
				}
			}
			if !hit {
				continue
			}
		}
		if flt["isfolder"] && !e.isFolder() || flt["isnotfolder"] && e.isFolder() {
			continue
		}
		if flt["isfavorite"] && !h.store.favorite(x.sess.Account, e.favKey()) {
			continue
		}
		if flt["isplayed"] || flt["isunplayed"] || flt["isresumable"] {
			played, resumable := false, false
			switch e.kind {
			case kItem:
				st := h.state(x, e.it)
				played, resumable = st.Played, st.resumable()
			case kShow, kSeason:
				total, p, _ := h.folderState(x, e)
				played = total > 0 && p == total
			}
			if flt["isplayed"] && !played || flt["isunplayed"] && played || flt["isresumable"] && !resumable {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// sortEntries: SortBy/SortOrder (erste unterstützte Angabe).
func (h *Handler) sortEntries(x *req, es []entry, def string) {
	by := x.QList("SortBy")
	order := x.QList("SortOrder")
	desc := len(order) > 0 && strings.EqualFold(order[0], "Descending")
	key := def
	for _, b := range by {
		switch strings.ToLower(b) {
		case "sortname", "name", "datecreated", "premieredate", "productionyear", "communityrating", "criticrating",
			"dateplayed", "random", "runtime", "indexnumber", "parentindexnumber", "seriessortname", "datelastcontentadded", "playcount":
			key = strings.ToLower(b)
		default:
			continue
		}
		break
	}
	if key == "random" {
		rand.Shuffle(len(es), func(i, j int) { es[i], es[j] = es[j], es[i] })
		return
	}
	names := make([]string, len(es))
	for i, e := range es {
		names[i] = e.sortName(h)
	}
	idx := make([]int, len(es))
	for i := range idx {
		idx[i] = i
	}
	num := func(i int) float64 {
		e := es[i]
		switch key {
		case "datecreated", "datelastcontentadded":
			switch e.kind {
			case kItem:
				return float64(e.it.AddedAt)
			case kShow, kSeason:
				var newest int64 = e.sh.AddedAt
				for _, ep := range h.episodes(x)[e.sh.ID] {
					if (e.kind == kShow || ep.Season == e.season) && ep.AddedAt > newest {
						newest = ep.AddedAt
					}
				}
				return float64(newest)
			}
		case "premieredate":
			if t, ok := parseDate(e.released()); ok {
				return float64(t.Unix())
			}
			if y := e.year(); y > 0 {
				return float64(time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
			}
		case "productionyear":
			return float64(e.year())
		case "communityrating", "criticrating":
			return e.rating()
		case "dateplayed":
			return float64(h.lastPlayed(x, e))
		case "runtime":
			if e.kind == kItem {
				return h.state(x, e.it).Dur
			}
		case "playcount":
			if e.kind == kItem && h.state(x, e.it).Played {
				return 1
			}
		case "indexnumber":
			switch e.kind {
			case kItem:
				return float64(e.it.Season*10000 + e.it.Episode)
			case kSeason:
				return float64(e.season)
			}
		case "parentindexnumber":
			if e.kind == kItem {
				return float64(e.it.Season*10000 + e.it.Episode)
			}
		}
		return 0
	}
	var vals []float64
	if key != "sortname" && key != "name" && key != "seriessortname" {
		vals = make([]float64, len(es))
		for i := range es {
			vals[i] = num(i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		i, j := idx[a], idx[b]
		if vals != nil && vals[i] != vals[j] {
			if desc {
				return vals[i] > vals[j]
			}
			return vals[i] < vals[j]
		}
		if vals == nil && desc {
			return names[i] > names[j]
		}
		return names[i] < names[j]
	})
	sorted := make([]entry, len(es))
	for k, i := range idx {
		sorted[k] = es[i]
	}
	copy(es, sorted)
}

// page: StartIndex/Limit.
func (x *req) page(n int) (start, end int) {
	start = x.QInt("StartIndex", 0)
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	end = n
	if lim := x.QInt("Limit", 0); lim > 0 && start+lim < n {
		end = start + lim
	}
	return
}

func (h *Handler) result(x *req, es []entry, o dtoOpts) map[string]any {
	start, end := x.page(len(es))
	items := make([]map[string]any, 0, end-start)
	for _, e := range es[start:end] {
		items = append(items, h.dto(x, e, o))
	}
	return map[string]any{"Items": items, "TotalRecordCount": len(es), "StartIndex": start}
}

// items: /Items und /Users/{id}/Items.
func (h *Handler) items(x *req) {
	types := lowerSet(x.QList("IncludeItemTypes"))
	excl := lowerSet(x.QList("ExcludeItemTypes"))
	recursive, _ := x.QBool("Recursive")
	var es []entry
	if ids := x.QList("Ids"); len(ids) > 0 {
		for _, id := range ids {
			if e, ok := h.resolve(x, id); ok {
				es = append(es, e)
			}
		}
	} else {
		es = h.collect(x, x.Q("ParentId"), types, recursive)
	}
	es = h.filter(x, es, types, excl)
	def := "sortname"
	h.sortEntries(x, es, def)
	writeJSON(x.w, h.result(x, es, x.dtoOpts()))
}

// item: Einzelabruf.
func (h *Handler) item(x *req, id string) {
	e, ok := h.resolve(x, id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	o := x.dtoOpts()
	o.full = true
	writeJSON(x.w, h.dto(x, e, o))
}

func (h *Handler) userViews(x *req) {
	var es []entry
	for _, v := range h.views(x) {
		es = append(es, entry{kind: kView, view: v})
	}
	writeJSON(x.w, h.result(x, es, x.dtoOpts()))
}

func (h *Handler) ancestors(x *req, id string) {
	e, ok := h.resolve(x, id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	out := []map[string]any{}
	o := x.dtoOpts()
	for {
		pid := h.parentID(e)
		if pid == "" || e.kind == kView || e.kind == kRoot {
			break
		}
		p, ok := h.resolve(x, pid)
		if !ok {
			break
		}
		out = append(out, h.dto(x, p, o))
		e = p
	}
	writeJSON(x.w, out)
}

// ---------- Serien ----------

func (h *Handler) showSeasons(x *req, id string) {
	k, p, ok := parseID(id)
	sh, found := library.Show{}, false
	if ok && k == kShow {
		sh, found = x.view.Show(int(p))
	}
	if !found {
		http.NotFound(x.w, x.r)
		return
	}
	es := h.seasonEntries(x, sh)
	es = h.filter(x, es, nil, nil)
	writeJSON(x.w, h.result(x, es, x.dtoOpts()))
}

func (h *Handler) showEpisodes(x *req, id string) {
	k, p, ok := parseID(id)
	sh, found := library.Show{}, false
	if ok && k == kShow {
		sh, found = x.view.Show(int(p))
	}
	if !found {
		http.NotFound(x.w, x.r)
		return
	}
	var season *int
	if sid := x.Q("SeasonId"); sid != "" {
		if sk, sp, ok := parseID(sid); ok && sk == kSeason {
			n := int(uint16(sp))
			season = &n
		}
	} else if s := x.Q("Season"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			season = &n
		}
	}
	es := h.episodeEntries(x, sh.ID, season, nil)
	es = h.filter(x, es, nil, nil)
	h.sortEntries(x, es, "indexnumber")
	if start := x.Q("StartItemId"); start != "" {
		for i, e := range es {
			if normID(e.id()) == normID(start) {
				es = es[i:]
				break
			}
		}
	}
	writeJSON(x.w, h.result(x, es, x.dtoOpts()))
}

// nextUp: je Serie die nächste Folge nach der zuletzt fertig gesehenen.
func (h *Handler) nextUp(x *req) {
	var lib *int
	if v, ok := h.findView(x.Q("ParentId")); ok {
		lib = &v.Lib
	}
	only := 0
	if k, p, ok := parseID(x.Q("SeriesId")); ok && k == kShow {
		only = int(p)
	}
	type cand struct {
		e    entry
		last int64
	}
	var cands []cand
	for showID, eps := range h.episodes(x) {
		if only != 0 && showID != only {
			continue
		}
		if len(eps) == 0 || (lib != nil && eps[0].LibraryID != *lib) {
			continue
		}
		sorted := append([]library.Item(nil), eps...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Season != sorted[j].Season {
				return sorted[i].Season < sorted[j].Season
			}
			return sorted[i].Episode < sorted[j].Episode
		})
		lastIdx, last := -1, int64(0)
		for i, ep := range sorted {
			if st := h.state(x, ep); st.Last > last {
				lastIdx, last = i, st.Last
			}
		}
		if lastIdx < 0 {
			continue
		}
		if st := h.state(x, sorted[lastIdx]); !st.Played {
			continue // läuft noch → „Fortsetzen“
		}
		for _, ep := range sorted[lastIdx+1:] {
			if ep.Season == 0 && sorted[lastIdx].Season != 0 {
				continue
			}
			if !h.state(x, ep).Played {
				cands = append(cands, cand{entry{kind: kItem, it: ep}, last})
				break
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].last > cands[j].last })
	es := make([]entry, len(cands))
	for i, c := range cands {
		es[i] = c.e
	}
	writeJSON(x.w, h.result(x, es, x.dtoOpts()))
}

// resume: angefangene Titel, zuletzt gesehene zuerst.
func (h *Handler) resume(x *req) {
	types := lowerSet(x.QList("IncludeItemTypes"))
	if len(types) == 0 {
		types = map[string]bool{"movie": true, "episode": true, "video": true}
	}
	es := h.collect(x, x.Q("ParentId"), types, true)
	var out []entry
	for _, e := range es {
		if e.kind == kItem && h.state(x, e.it).resumable() {
			out = append(out, e)
		}
	}
	out = h.filter(x, out, types, lowerSet(x.QList("ExcludeItemTypes")))
	sort.SliceStable(out, func(i, j int) bool { return h.state(x, out[i].it).Last > h.state(x, out[j].it).Last })
	writeJSON(x.w, h.result(x, out, x.dtoOpts()))
}

// latest: zuletzt hinzugefügt (Array, nicht QueryResult). Serien: mehrere neue
// Folgen einer Serie erscheinen als Serie, eine einzelne als Folge.
func (h *Handler) latest(x *req) {
	limit := x.QInt("Limit", 16)
	if limit <= 0 || limit > 100 {
		limit = 16
	}
	o := x.dtoOpts()
	var es []entry
	var views []viewInfo
	if v, ok := h.findView(x.Q("ParentId")); ok {
		views = []viewInfo{v}
	} else {
		views = h.views(x)
	}
	types := lowerSet(x.QList("IncludeItemTypes"))
	group := true
	if v, ok := x.QBool("GroupItems"); ok {
		group = v
	}
	for _, v := range views {
		lib := v.Lib
		if v.Typ != "tvshow" {
			for _, e := range h.allOfLib(x, v.Typ, &lib) {
				if len(types) > 0 && !types[e.typeKey()] {
					continue
				}
				if !h.state(x, e.it).Played {
					es = append(es, e)
				}
			}
			continue
		}
		eps := h.episodeEntries(x, 0, nil, &lib)
		sort.SliceStable(eps, func(i, j int) bool { return eps[i].it.AddedAt > eps[j].it.AddedAt })
		perShow := map[int][]entry{}
		var order []int
		for _, e := range eps {
			if h.state(x, e.it).Played {
				continue
			}
			if _, seen := perShow[e.it.ShowID]; !seen {
				order = append(order, e.it.ShowID)
			}
			perShow[e.it.ShowID] = append(perShow[e.it.ShowID], e)
		}
		for _, sid := range order {
			list := perShow[sid]
			newest := list[0].it.AddedAt
			n := 0
			for _, e := range list {
				if newest-e.it.AddedAt < 24*3600 {
					n++
				}
			}
			if (n > 1 && group) || types["series"] {
				if sh, ok := x.view.Show(sid); ok {
					se := entry{kind: kShow, sh: sh}
					se.sh.AddedAt = newest
					es = append(es, se)
				}
				continue
			}
			es = append(es, list[0])
		}
	}
	added := func(e entry) int64 {
		if e.kind == kShow {
			return e.sh.AddedAt
		}
		return e.it.AddedAt
	}
	sort.SliceStable(es, func(i, j int) bool { return added(es[i]) > added(es[j]) })
	if len(es) > limit {
		es = es[:limit]
	}
	out := make([]map[string]any, 0, len(es))
	for _, e := range es {
		out = append(out, h.dto(x, e, o))
	}
	writeJSON(x.w, out)
}

// genres: Genres der sichtbaren Titel (optional einer Bibliothek).
func (h *Handler) genres(x *req) {
	types := lowerSet(x.QList("IncludeItemTypes"))
	es := h.collect(x, x.Q("ParentId"), types, true)
	count := map[string]int{}
	name := map[string]string{}
	for _, e := range es {
		for _, g := range e.genres() {
			k := strings.ToLower(g)
			count[k]++
			name[k] = g
		}
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	start, end := x.page(len(keys))
	items := []map[string]any{}
	for _, k := range keys[start:end] {
		items = append(items, map[string]any{"Name": name[k], "Id": nameID(kGenre, name[k]), "ServerId": h.store.serverID(),
			"Type": "Genre", "IsFolder": true, "ImageTags": map[string]string{}, "BackdropImageTags": []string{},
			"ChildCount": count[k], "LocationType": "Virtual"})
	}
	writeJSON(x.w, map[string]any{"Items": items, "TotalRecordCount": len(keys), "StartIndex": start})
}

// searchHints: ältere Suchschnittstelle.
func (h *Handler) searchHints(x *req) {
	term := strings.ToLower(strings.TrimSpace(x.Q("SearchTerm")))
	types := lowerSet(x.QList("IncludeItemTypes"))
	if len(types) == 0 {
		types = map[string]bool{"movie": true, "series": true, "episode": true, "video": true}
	}
	var hits []map[string]any
	if term != "" {
		es := h.filter(x, h.collect(x, "", types, true), types, nil)
		h.sortEntries(x, es, "sortname")
		start, end := x.page(len(es))
		for _, e := range es[start:end] {
			d := h.dto(x, e, dtoOpts{})
			hint := map[string]any{"ItemId": d["Id"], "Id": d["Id"], "Name": d["Name"], "Type": d["Type"], "IsFolder": d["IsFolder"],
				"MediaType": d["MediaType"], "ProductionYear": d["ProductionYear"], "RunTimeTicks": d["RunTimeTicks"],
				"Series": d["SeriesName"], "IndexNumber": d["IndexNumber"], "ParentIndexNumber": d["ParentIndexNumber"]}
			if tags, _ := d["ImageTags"].(map[string]string); tags["Primary"] != "" {
				hint["PrimaryImageTag"] = tags["Primary"]
			}
			hits = append(hits, hint)
		}
		writeJSON(x.w, map[string]any{"SearchHints": hits, "TotalRecordCount": len(es)})
		return
	}
	writeJSON(x.w, map[string]any{"SearchHints": []any{}, "TotalRecordCount": 0})
}

// similar: Titel mit gemeinsamen Genres.
func (h *Handler) similar(x *req, id string) {
	e, ok := h.resolve(x, id)
	if !ok || (e.kind != kItem && e.kind != kShow) {
		writeJSON(x.w, emptyResult())
		return
	}
	want := map[string]bool{}
	for _, g := range e.genres() {
		want[strings.ToLower(g)] = true
	}
	var pool []entry
	if e.kind == kShow {
		pool = h.allOfLib(x, "tvshow", nil)
	} else {
		pool = h.allOfLib(x, e.it.Kind, nil)
	}
	type scored struct {
		e entry
		n int
	}
	var list []scored
	for _, c := range pool {
		if c.id() == e.id() {
			continue
		}
		n := 0
		for _, g := range c.genres() {
			if want[strings.ToLower(g)] {
				n++
			}
		}
		if n > 0 {
			list = append(list, scored{c, n})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].e.rating() > list[j].e.rating()
	})
	limit := x.QInt("Limit", 12)
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	if len(list) > limit {
		list = list[:limit]
	}
	es := make([]entry, len(list))
	for i, s := range list {
		es[i] = s.e
	}
	writeJSON(x.w, map[string]any{"Items": h.dtos(x, es), "TotalRecordCount": len(es), "StartIndex": 0})
}

func (h *Handler) dtos(x *req, es []entry) []map[string]any {
	o := x.dtoOpts()
	out := make([]map[string]any, 0, len(es))
	for _, e := range es {
		out = append(out, h.dto(x, e, o))
	}
	return out
}

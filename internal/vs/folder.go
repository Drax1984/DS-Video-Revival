package vs

// Ordnerbrowser („Nach Ordner“): SYNO.VideoStation.Folder list (Android)
// und SYNO.VideoStation2.Folder list (iOS).
//
// Belegt aus DS video 3.4.7 (FetchFolderContentTask, FolderContentVo,
// FolderContent, Addition, FolderContentListFragment):
//   Anfrage:  library_id, offset, limit, sort_by=title, sort_direction=asc,
//             id ("" = Wurzel, sonst id eines Ordnerobjekts), type
//             (Bibliothekstyp), v2 zusätzlich additional=["watched_ratio",
//             "backdrop_mtime","poster_mtime"] und preview_video=4.
//   Antwort:  data{objects[], offset, total}; Objekt: id (String), title,
//             type ("folder" = Ordner), file_count (int), preview_video
//             (Liste), additional{metadata: VideoV2Vo} (nur Dateien).
//   Die App sortiert Ordner vor Dateien (Abschnittsköpfe) und öffnet eine
//   Datei über additional.metadata.id + id als file_id.
// preview_video (Ordnerkachel als Plakat-Collage): Liste von PreviewVideo
// {id, type, additional{poster_mtime}} – belegt aus DS video 3.4.7
// (vos/video/PreviewVideo, widget/CoverCollageView: höchstens 4 Bilder,
// Plakat über SYNO.VideoStation.Poster getimage id/type; die App setzt als
// Typ den der Bibliothek, bei Serien tvshow_episode).
// Nicht belegt (eigene Festlegung): Kodierung der Ordner-IDs, Bedeutung von
// file_count (hier: Videodateien im Ordner samt Unterordnern), Auswahl der
// Vorschaubilder (zufällig bei jedem Abruf, je Serie höchstens eins).
// Eigene Festlegung (Wunsch Nutzer, 0.5.25): Die Wurzel zeigt die
// Medienordner; ein geöffneter Ordner zeigt alle Medien darin samt
// Unterordnern als flache Liste (kein Durchklicken), Titel wie in „Alle“.

import (
	"encoding/base64"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/webapi"
)

func encodeFolderID(p string) string { return "d" + base64.RawURLEncoding.EncodeToString([]byte(p)) }

func decodeFolderID(id string) (string, bool) {
	if !strings.HasPrefix(id, "d") {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(id[1:])
	if err != nil || len(b) == 0 {
		return "", false
	}
	return filepath.Clean(string(b)), true
}

// kindType: Objektart → VideoType-Name der App (Common.VideoType).
func kindType(kind string) string {
	switch kind {
	case "episode":
		return "tvshow_episode"
	case "home_video":
		return "home_video"
	}
	return "movie"
}

// folderListV1: SYNO.VideoStation.Folder list (Android), Schlüssel "objects".
func folderListV1(c *Ctx) { folderListing(c, false) }

// folderListV2: SYNO.VideoStation2.Folder list (iOS u. a.), Schlüssel "object";
// Ordnerobjekte laut Schema SYNO.VideoStation2.Folder-list (Q3): file_count,
// id (String), path, sharepath, title, type. Dateiobjekte sind dort nicht
// belegt – gleicher Aufbau wie in der VS1-Antwort, ergänzt um path/sharepath.
func folderListV2(c *Ctx) { folderListing(c, true) }

func folderListing(c *Ctx, v2 bool) {
	add := recordAdditional(c)
	libType, libID := c.Req.Str("type"), c.Req.Int("library_id", 0)
	var roots []string
	for _, f := range c.S.Cfg().Folders {
		if (libType == "" || f.Type == libType) && f.LibraryID == libID && c.Lib.FolderAllowed(f.Path) {
			roots = append(roots, filepath.Clean(f.Path))
		}
	}
	desc := strings.EqualFold(c.Req.Str("sort_direction"), "desc")
	less := func(a, b string) bool {
		if desc {
			a, b = b, a
		}
		return strings.ToLower(a) < strings.ToLower(b)
	}

	type folderObj struct {
		path    string
		title   string
		count   int
		preview []map[string]any
	}
	var folders []folderObj
	var files []map[string]any
	var fileTitles []string

	dir, ok, id := resolveFolder(c, roots)
	switch {
	case ok && inAny(dir, roots):
		// Alle Medien im Ordner samt Unterordnern, ohne Unterordner-Objekte.
		shows := map[int]string{}
		for _, e := range c.Lib.FilesUnder(dir) {
			title := folderItemTitle(c, e.Item, shows)
			meta := videoObject(c, e.Item, add, false)
			meta["type"] = kindType(e.Item.Kind)
			o := map[string]any{"id": strconv.Itoa(e.File.ID), "title": title,
				"type": kindType(e.Item.Kind), "file_count": 0, "preview_video": []any{},
				"additional": map[string]any{"metadata": meta}}
			if v2 {
				o["path"], o["sharepath"] = e.File.Path, sharePath(e.File.Path)
			}
			files = append(files, o)
			fileTitles = append(fileTitles, library.SortTitle(title)+"\x00"+filepath.Base(e.File.Path))
		}
	case ok:
		c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, "Ordner außerhalb der Medienordner – leere Liste", paramKeys(c.Req.Params))
	default:
		// Wurzel ("" oder unbekannte ID): Medienordner dieser Bibliothek.
		nPrev := c.Req.Int("preview_video", 4)
		for _, r := range roots {
			entries := c.Lib.FilesUnder(r)
			folders = append(folders, folderObj{path: r, title: filepath.Base(r), count: len(entries), preview: previewVideos(c, entries, nPrev)})
		}
	}

	sort.SliceStable(folders, func(i, j int) bool { return less(folders[i].title, folders[j].title) })
	idx := make([]int, len(files))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return less(fileTitles[idx[i]], fileTitles[idx[j]]) })

	objs := make([]map[string]any, 0, len(folders)+len(files))
	for _, f := range folders {
		pv := f.preview
		if pv == nil {
			pv = []map[string]any{}
		}
		o := map[string]any{"id": encodeFolderID(f.path), "title": f.title, "type": "folder",
			"file_count": f.count, "preview_video": pv, "additional": map[string]any{}}
		if v2 {
			o["path"], o["sharepath"] = f.path, sharePath(f.path)
		}
		objs = append(objs, o)
	}
	for _, i := range idx {
		objs = append(objs, files[i])
	}

	total := len(objs)
	off, lim := c.Req.Int("offset", 0), c.Req.Int("limit", 0)
	if off < 0 {
		off = 0
	}
	if off > total {
		off = total
	}
	objs = objs[off:]
	if lim > 0 && lim < len(objs) {
		objs = objs[:lim]
	}
	log.Printf("ordner: %s v%d (Konto %q) %s → Ordner %q: %d Unterordner, %d Dateien",
		c.Req.API, c.Req.Version, c.Sess.Account, paramDump(c), id, len(folders), len(files))
	key := "objects"
	if v2 {
		key = "object"
	}
	webapi.OK(c.W, map[string]any{key: objs, "offset": off, "total": total})
}

// previewVideos: bis zu n (höchstens 4) zufällig gewählte Titel mit Plakat –
// bei jedem Abruf andere, Serien nur einmal.
func previewVideos(c *Ctx, entries []library.FileEntry, n int) []map[string]any {
	if n > 4 {
		n = 4
	}
	if n <= 0 {
		return nil
	}
	seen := map[string]bool{}
	var cands []library.Item
	for _, e := range entries {
		it := e.Item
		key, poster := "i"+strconv.Itoa(it.ID), it.PosterPath()
		if it.Kind == "episode" {
			key = "s" + strconv.Itoa(it.ShowID)
			if poster == "" {
				if sh, ok := c.Lib.Show(it.ShowID); ok {
					poster = showImage(sh, false)
				}
			}
		}
		if poster == "" || seen[key] {
			continue
		}
		if _, err := os.Stat(poster); err != nil {
			continue
		}
		seen[key] = true
		cands = append(cands, it)
	}
	rand.Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make([]map[string]any, 0, len(cands))
	for _, it := range cands {
		pv := map[string]any{"id": strconv.Itoa(it.ID), "type": kindType(it.Kind)}
		if t, ok := mtimeOf(it.PosterPath()); ok && c.S.Cfg().SendPosterMtime {
			pv["additional"] = map[string]any{"poster_mtime": t}
		}
		out = append(out, pv)
	}
	return out
}

// folderItemTitle: Titel wie in „Alle“; Episoden als „Serie – S01E02“.
func folderItemTitle(c *Ctx, it library.Item, shows map[int]string) string {
	if it.Kind != "episode" {
		return it.DisplayTitle()
	}
	name, ok := shows[it.ShowID]
	if !ok {
		name = it.Show
		if sh, found := c.Lib.Show(it.ShowID); found {
			name = sh.DisplayTitle()
		}
		shows[it.ShowID] = name
	}
	ep := fmt.Sprintf("S%02dE%02d", it.Season, it.Episode)
	if it.EpisodeEnd > it.Episode {
		ep += fmt.Sprintf("-E%02d", it.EpisodeEnd)
	}
	return name + " – " + ep
}

// folderParams: Parameter, in denen die App den geöffneten Ordner übergeben
// kann. Belegt ist nur "id" (Android); iOS ist nicht dokumentiert, daher
// werden auch Pfadangaben (path/sharepath aus dem Ordnerobjekt) akzeptiert.
var folderParams = []string{"id", "folder_id", "path", "folder_path", "sharepath", "folder"}

// resolveFolder: geöffneter Ordner aus der Anfrage. ok=false → Wurzel.
func resolveFolder(c *Ctx, roots []string) (dir string, ok bool, raw string) {
	// erst die bekannten Namen, dann alle übrigen Parameter (Name bei iOS unbelegt)
	keys := append([]string{}, folderParams...)
	for _, k := range paramKeys(c.Req.Params) {
		switch k {
		case "type", "library_id", "offset", "limit", "sort_by", "sort_direction", "additional", "preview_video":
			continue
		}
		keys = append(keys, k)
	}
	for _, k := range keys {
		v := strings.TrimSpace(c.Req.Str(k))
		if strings.HasPrefix(v, "[") {
			if l := c.Req.StrList(k); len(l) > 0 {
				v = l[0]
			}
		}
		if v == "" {
			continue
		}
		if d, ok := decodeFolderID(v); ok {
			return d, true, v
		}
		if strings.HasPrefix(v, "/") {
			p := filepath.Clean(v)
			if inAny(p, roots) {
				return p, true, v
			}
			for _, r := range roots { // Freigabepfad (/video/…) → Volume-Pfad
				sp := sharePath(r)
				if p == sp || strings.HasPrefix(p, sp+"/") {
					return filepath.Join(r, strings.TrimPrefix(p, sp)), true, v
				}
			}
		}
		raw = v
	}
	return "", false, raw
}

// paramDump: Anfrageparameter mit Werten fürs Protokoll (ohne Sitzungsdaten).
func paramDump(c *Ctx) string {
	var parts []string
	for _, k := range paramKeys(c.Req.Params) {
		v := c.Req.Params.Get(k)
		if len(v) > 120 {
			v = v[:120] + "…"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

func inAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

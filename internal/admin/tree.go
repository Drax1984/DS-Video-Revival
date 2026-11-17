package admin

// Ordneransicht und Dateipfade – nur für DSM-Administratoren (nicht für die
// eigene Administratorengruppe von DS Video Revival): Pfade auf der NAS sind
// Systeminformationen.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
)

type treeFile struct {
	ID     int    `json:"id"`      // Objekt-ID (Detailansicht)
	FileID int    `json:"file_id"` // Datei-ID (Löschen)
	Kind   string `json:"kind"`
	ShowID int    `json:"show_id,omitempty"`
	Title  string `json:"title"`
	Sub    string `json:"sub,omitempty"` // Jahr bzw. SxxExx und Folgentitel
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Poster string `json:"poster,omitempty"`
	// Technik zum Vergleich von Doppelgängern
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	VCodec string  `json:"vcodec,omitempty"`
	ACodec string  `json:"acodec,omitempty"`
	Dur    float64 `json:"duration,omitempty"`
	// Dup: Schlüssel gleicher Titel (TMDb-ID bzw. Objekt, bei Serien Folge);
	// Dups: Anzahl Dateien mit diesem Schlüssel über alle Medienordner.
	Dup     string `json:"dup"`
	Dups    int    `json:"dups"`
	Ignored bool   `json:"ignored,omitempty"` // bei Doppelgängern ignoriert (zählt nicht mit)
	Recycle bool   `json:"recycle"`           // Papierkorb der Freigabe aktiv
}

type treeDir struct {
	Path  string     `json:"path"`
	Rel   string     `json:"rel"` // relativ zum Medienordner ("" = Medienordner selbst)
	Files []treeFile `json:"files"`
}

type treeRoot struct {
	Path    string    `json:"path"`
	Library string    `json:"library"`
	Count   int       `json:"count"`
	Dirs    []treeDir `json:"dirs"`
}

var stdLibNames = map[string]string{"movie": "Filme", "tvshow": "Serien", "home_video": "Heimvideos", "tv_record": "TV-Aufnahmen"}

func libLabel(c *config.Config, f config.Folder) string {
	if f.LibraryID > 0 {
		for _, l := range c.Libraries {
			if l.ID == f.LibraryID {
				return l.Title
			}
		}
	}
	if n, ok := stdLibNames[f.Type]; ok {
		return n
	}
	return f.Type
}

func showPoster(sh library.Show) string {
	return imgURL("tvshow", sh.ID, "poster", sh.PosterPath())
}

// tree: GET /dsvr/api/tree – Medienordner → Unterordner → Dateien mit Titel.
func (h *Handler) tree(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !h.S.Auth.IsAdmin(sess) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"error": "Nur für DSM-Administratoren"})
		return
	}
	cfg := h.S.Cfg()
	v := h.lib(sess)
	shows := map[int]library.Show{}
	show := func(id int) (library.Show, bool) {
		if sh, ok := shows[id]; ok {
			return sh, true
		}
		sh, ok := v.Show(id)
		if ok {
			shows[id] = sh
		}
		return sh, ok
	}
	posters := map[string]string{} // Plakat-URL je Objekt (imgURL prüft die Datei)
	dupCount := map[string]int{}
	recycle := map[string]bool{}
	ignored := map[string]bool{}
	for _, p := range cfg.DupIgnore {
		ignored[filepath.Clean(p)] = true
	}
	out := []treeRoot{}
	for _, f := range cfg.Folders {
		root := filepath.Clean(f.Path)
		tr := treeRoot{Path: root, Library: libLabel(cfg, f), Dirs: []treeDir{}}
		byDir := map[string][]treeFile{}
		for _, e := range v.FilesUnder(root) {
			it := e.Item
			tf := treeFile{ID: it.ID, FileID: e.File.ID, Kind: it.Kind, Name: filepath.Base(e.File.Path), Path: e.File.Path, Size: e.File.Size}
			if pr := e.File.Probe; pr != nil {
				tf.Width, tf.Height, tf.VCodec, tf.ACodec, tf.Dur = pr.Width, pr.Height, pr.VideoCodec, pr.AudioCodec, pr.Duration
			}
			tf.Dup = dupKey(it, e.File.Path) + partKey(e.File.Path)
			if tf.Ignored = ignored[filepath.Clean(e.File.Path)]; !tf.Ignored {
				dupCount[tf.Dup]++
			}
			share := shareRoot(e.File.Path)
			rc, have := recycle[share]
			if !have {
				rc = recycleAvailable(e.File.Path)
				recycle[share] = rc
			}
			tf.Recycle = rc
			key := it.Kind + ":" + strconv.Itoa(it.ID)
			if it.Kind == "episode" {
				tf.ShowID, tf.Title = it.ShowID, it.Show
				ep := fmt.Sprintf("S%02dE%02d", it.Season, it.Episode)
				if it.EpisodeEnd > it.Episode {
					ep += fmt.Sprintf("-E%02d", it.EpisodeEnd)
				}
				tf.Sub = strings.TrimSuffix(ep+" · "+it.EpisodeTitle(), " · ")
				key = "tvshow:" + strconv.Itoa(it.ShowID)
				if sh, ok := show(it.ShowID); ok {
					tf.Title = sh.DisplayTitle()
					if _, have := posters[key]; !have {
						posters[key] = showPoster(sh)
					}
				}
			} else {
				tf.Title = it.DisplayTitle()
				if it.Kind == "tv_record" && it.Channel != "" {
					tf.Sub = it.Channel
				} else if y := metaYear(it.Meta, it.Year); y > 0 {
					tf.Sub = strconv.Itoa(y)
				}
				if _, have := posters[key]; !have {
					posters[key] = imgURL(it.Kind, it.ID, "poster", it.PosterPath())
				}
			}
			tf.Poster = posters[key]
			d := filepath.Dir(e.File.Path)
			byDir[d] = append(byDir[d], tf)
			tr.Count++
		}
		for d, files := range byDir {
			rel, err := filepath.Rel(root, d)
			if err != nil || rel == "." {
				rel = ""
			}
			sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })
			tr.Dirs = append(tr.Dirs, treeDir{Path: d, Rel: rel, Files: files})
		}
		sort.Slice(tr.Dirs, func(i, j int) bool { return strings.ToLower(tr.Dirs[i].Rel) < strings.ToLower(tr.Dirs[j].Rel) })
		out = append(out, tr)
	}
	for i := range out {
		for j := range out[i].Dirs {
			for k := range out[i].Dirs[j].Files {
				f := &out[i].Dirs[j].Files[k]
				if !f.Ignored {
					f.Dups = dupCount[f.Dup]
				}
			}
		}
	}
	writeJSON(w, out)
}

// dupIgnoreMu: Lesen-Ändern-Schreiben der Ignorierliste nacheinander.
var dupIgnoreMu sync.Mutex

// dupIgnore: POST /dsvr/api/dup-ignore {file_id, ignore} – Datei bei der
// Doppelgänger-Suche ignorieren bzw. wieder berücksichtigen (DSM-Administratoren).
func (h *Handler) dupIgnore(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if !h.S.Auth.IsAdmin(sess) {
		jsonErr(w, http.StatusForbidden, "Nur für DSM-Administratoren")
		return
	}
	var req struct {
		FileID int  `json:"file_id"`
		Ignore bool `json:"ignore"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	f, ok := h.S.Lib.File(req.FileID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p := filepath.Clean(f.Path)
	dupIgnoreMu.Lock()
	defer dupIgnoreMu.Unlock()
	c := *h.S.Cfg()
	list := []string{}
	for _, x := range c.DupIgnore {
		if filepath.Clean(x) != p {
			list = append(list, x)
		}
	}
	if req.Ignore {
		list = append(list, p)
		sort.Strings(list)
	}
	c.DupIgnore = list
	if len(list) == 0 {
		c.DupIgnore = nil
	}
	if h.CfgPath != "" {
		if err := config.Save(h.CfgPath, c); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.S.SetCfg(c)
	if req.Ignore {
		log.Printf("ordneransicht: %s ignoriert %s bei Doppelgängern", sess.Account, p)
	} else {
		log.Printf("ordneransicht: %s berücksichtigt %s wieder bei Doppelgängern", sess.Account, p)
	}
	writeJSON(w, map[string]any{"ok": true, "ignored": req.Ignore})
}

// dupKey: Dateien mit gleichem Schlüssel gelten als Doppelgänger – gleicher
// Film laut TMDb, gleiche Folge einer Serie oder mehrere Dateien desselben
// Titels (z.B. Film und „Sample“).
func dupKey(it library.Item, path string) string {
	switch {
	case it.Kind == "episode" && it.Episode > 0:
		return fmt.Sprintf("ep:%d:%d:%d", it.ShowID, it.Season, it.Episode)
	case it.Kind == "episode" && dvdKey(path) != "":
		// DVD-Ordner einer Serie (Folgennummer unbekannt): Vergleich über Disc
		// und VOB-Datei (partKey), innerhalb derselben Serie.
		return "dvd:" + strconv.Itoa(it.ShowID)
	// Sonstige Folge ohne erkannte Nummer (S00E00): nicht vergleichbar – nur
	// Dateien desselben Eintrags.
	case it.Kind == "movie" && it.Meta != nil && it.Meta.TMDbID > 0:
		return "tmdb:" + strconv.Itoa(it.Meta.TMDbID)
	}
	return "item:" + strconv.Itoa(it.ID)
}

// rePart: Teilangabe mehrteiliger Filme (z.B. von CD eingelesen):
// „Film CD1.avi“, „film.part2.mkv“, „Film - Teil 2“, „film-disc1“, „film.pt.2“.
var rePart = regexp.MustCompile(`(?i)(?:^|[\s._\-\[(])(cd|dvd|disc|disk|part|pt|teil)[\s._\-]*([0-9]{1,2}|[a-d])(?:$|[\s._\-\])])`)
var rePartDir = regexp.MustCompile(`(?i)^(cd|dvd|disc|disk|part|pt|teil)[\s._\-]*([0-9]{1,2}|[a-d])$`)

// reOf: „1von3.mpg“, „Film 2 von 3“, „film.1of2.avi“ (Teil 1 von 3).
var reOf = regexp.MustCompile(`(?i)(?:^|[\s._\-\[(])([0-9]{1,2})[\s._\-]*(?:von|of|aus)[\s._\-]*([0-9]{1,2})(?:$|[\s._\-\])])`)

// partKey: Teil einer mehrteiligen Datei (":teil2"), sonst "" – Teile eines
// Films sind keine Doppelgänger voneinander.
func partKey(p string) string {
	if k := dvdKey(p); k != "" {
		return k
	}
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	var n string
	if ms := rePart.FindAllStringSubmatch(base, -1); len(ms) > 0 {
		n = ms[len(ms)-1][2] // letzte Angabe (Titel wie „Part 2“ stehen vorn)
	} else if ms := reOf.FindAllStringSubmatch(base, -1); len(ms) > 0 {
		n = ms[len(ms)-1][1] // „1 von 3“ → Teil 1
	} else if m := rePartDir.FindStringSubmatch(filepath.Base(filepath.Dir(p))); m != nil {
		n = m[2]
	} else {
		return ""
	}
	n = strings.ToLower(n)
	if len(n) == 1 && n[0] >= 'a' && n[0] <= 'd' {
		n = strconv.Itoa(int(n[0]-'a') + 1)
	}
	n = strings.TrimLeft(n, "0")
	if n == "" {
		n = "0"
	}
	return ":teil" + n
}

// dvdKey: DVD-Struktur (…/DISC_3/VIDEO_TS/VTS_01_2.VOB) – jede VOB-Datei ist
// ein eigener Abschnitt einer bestimmten Disc. Schlüssel aus Disc-Ordner und
// Dateiname: verschiedene Discs bzw. Abschnitte sind keine Doppelgänger,
// dieselbe Disc an zwei Stellen schon.
func dvdKey(p string) string {
	dir := filepath.Dir(p)
	if !strings.EqualFold(filepath.Ext(p), ".vob") && !strings.EqualFold(filepath.Base(dir), "VIDEO_TS") {
		return ""
	}
	disc := dir
	if strings.EqualFold(filepath.Base(dir), "VIDEO_TS") {
		disc = filepath.Dir(dir)
	}
	return ":dvd:" + strings.ToLower(filepath.Base(disc)+"/"+filepath.Base(p))
}

// shareRoot: /volumeX/freigabe einer Datei (Schlüssel für den Papierkorb).
func shareRoot(p string) string {
	vol, share, _, ok := splitShare(filepath.Clean(p))
	if !ok {
		return filepath.Dir(p)
	}
	return filepath.Join(vol, share)
}

// showDirs: Ordner einer Serie (gemeinsamer Ordner der Folgen, falls vorhanden).
func showDirs(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var dirs []string
	for _, p := range paths {
		d := filepath.Dir(p)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	if len(dirs) == 1 {
		return dirs
	}
	// gemeinsamer Ordner, z.B. Serie/Staffel 1, Serie/Staffel 2 → Serie
	common := dirs[0]
	for _, d := range dirs[1:] {
		for common != "/" && common != "." && d != common && !strings.HasPrefix(d, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
		}
	}
	if common != "/" && common != "." && strings.Count(common, string(filepath.Separator)) > 2 {
		return []string{common}
	}
	if len(dirs) > 5 {
		dirs = dirs[:5]
	}
	return dirs
}

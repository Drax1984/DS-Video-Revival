package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/config"
)

// File is one physical video file.
type File struct {
	ID      int    `json:"id"`
	ItemID  int    `json:"item_id"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Folder  string `json:"folder"` // konfigurierter Wurzelordner
	Missing bool   `json:"-"`
	Probe   *Probe `json:"probe,omitempty"` // technische Daten (ffprobe), nachgeladen
}

// Item is a library object (Film, Episode, Heimvideo). Mehrere Dateien mit
// gleichem Titel/Jahr bzw. gleicher Folge werden einem Objekt zugeordnet
// (Duplikaterkennung, wie Video Station "mehrere Versionen").
type Item struct {
	ID         int    `json:"id"`
	Kind       string `json:"kind"` // movie | episode | home_video | tv_record
	LibraryID  int    `json:"library_id"`
	Title      string `json:"title"`
	SortTitle  string `json:"sort_title"`
	Year       int    `json:"year"`
	Show       string `json:"show,omitempty"`
	ShowID     int    `json:"show_id,omitempty"`
	Season     int    `json:"season,omitempty"`
	Episode    int    `json:"episode,omitempty"`
	EpisodeEnd int    `json:"episode_end,omitempty"`
	AddedAt    int64  `json:"added_at"`
	FileIDs    []int  `json:"file_ids"`
	Poster     string `json:"poster,omitempty"`
	Meta       *Meta  `json:"meta,omitempty"`
	FSK        *int   `json:"fsk,omitempty"` // manuell gesetzte FSK (nil = aus Metadaten)
	// CustomTitle: in der Weboberfläche von Hand gesetzter Titel (hat Vorrang).
	CustomTitle string `json:"custom_title,omitempty"`
	// TV-Aufnahmen: Sender und Aufnahmezeit (aus dem Dateinamen).
	Channel  string `json:"channel,omitempty"`
	Recorded string `json:"recorded,omitempty"`
}

// Show groups episodes.
type Show struct {
	ID        int    `json:"id"`
	LibraryID int    `json:"library_id"`
	Title     string `json:"title"`
	SortTitle string `json:"sort_title"`
	AddedAt   int64  `json:"added_at"`
	Poster    string `json:"poster,omitempty"`
	Meta      *Meta  `json:"meta,omitempty"`
	FSK       *int   `json:"fsk,omitempty"` // manuell gesetzte FSK
	// CustomTitle: in der Weboberfläche von Hand gesetzter Titel (hat Vorrang).
	CustomTitle string `json:"custom_title,omitempty"`
}

// WatchState per user and file.
type WatchState struct {
	Position  int   `json:"position"`
	UpdatedAt int64 `json:"updated_at"`
}

type persisted struct {
	NextID  int                           `json:"next_id"`
	Items   map[int]*Item                 `json:"items"`
	Files   map[int]*File                 `json:"files"`
	Shows   map[int]*Show                 `json:"shows"`
	Watch   map[string]map[int]WatchState `json:"watch"`             // account → file id → state
	Ratings map[string]map[string]float64 `json:"ratings,omitempty"` // account → "movie:12" → eigene Bewertung 1–10
	Version int                           `json:"version"`
}

// Index is the in-memory library, persisted as JSON (DB-Schicht der PoC;
// SQLite folgt, sobald ein reiner-Go-Treiber eingebunden wird).
type Index struct {
	mu       sync.RWMutex
	path     string
	data     persisted
	byPath   map[string]int // file path → file id
	byKey    map[string]int // Duplikat-/Zuordnungsschlüssel → item/show id
	scanning bool
	dirty    bool
	timer    *time.Timer
	LastScan time.Time
	LastErr  []string
}

const schemaVersion = 1

// Open loads or creates the index file.
func Open(path string) (*Index, error) {
	ix := &Index{path: path}
	ix.data = persisted{NextID: 1, Items: map[int]*Item{}, Files: map[int]*File{}, Shows: map[int]*Show{}, Watch: map[string]map[int]WatchState{}, Ratings: map[string]map[string]float64{}, Version: schemaVersion}
	if b, err := os.ReadFile(path); err == nil {
		var p persisted
		if err := json.Unmarshal(b, &p); err != nil {
			// Defekte Datei nicht überschreiben, sondern sichern.
			_ = os.Rename(path, path+".corrupt-"+time.Now().Format("20060102-150405"))
			log.Printf("library: Index defekt, gesichert und neu angelegt: %v", err)
		} else {
			if p.Items == nil {
				p.Items = map[int]*Item{}
			}
			if p.Files == nil {
				p.Files = map[int]*File{}
			}
			if p.Shows == nil {
				p.Shows = map[int]*Show{}
			}
			if p.Ratings == nil {
				p.Ratings = map[string]map[string]float64{}
			}
			if p.Watch == nil {
				p.Watch = map[string]map[int]WatchState{}
			}
			ix.data = p
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ix.rebuild()
	return ix, nil
}

func (ix *Index) rebuild() {
	ix.byPath = map[string]int{}
	ix.byKey = map[string]int{}
	for id, f := range ix.data.Files {
		ix.byPath[f.Path] = id
	}
	for id, it := range ix.data.Items {
		if k := itemKey(it); k != "" {
			ix.byKey[k] = id
		}
	}
	for id, sh := range ix.data.Shows {
		ix.byKey[showKey(sh.LibraryID, sh.Title)] = id
	}
}

func itemKey(it *Item) string {
	switch it.Kind {
	case "movie":
		return fmt.Sprintf("m|%d|%s|%d", it.LibraryID, strings.ToLower(it.Title), it.Year)
	case "episode":
		if it.Episode != 0 {
			return fmt.Sprintf("e|%d|%d|%d", it.ShowID, it.Season, it.Episode)
		}
	}
	return ""
}

func showKey(lib int, title string) string {
	return fmt.Sprintf("s|%d|%s", lib, strings.ToLower(title))
}

func (ix *Index) nextID() int {
	id := ix.data.NextID
	ix.data.NextID++
	return id
}

// saveLocked markiert den Index als geändert; geschrieben wird gebündelt
// spätestens nach 3 s (schont Platten bei vielen Einzeländerungen).
func (ix *Index) saveLocked() error {
	ix.dirty = true
	if ix.timer == nil {
		ix.timer = time.AfterFunc(3*time.Second, func() { _ = ix.Flush() })
	}
	return nil
}

// Flush writes pending changes immediately (atomar).
func (ix *Index) Flush() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.timer = nil
	if !ix.dirty {
		return nil
	}
	b, err := json.Marshal(ix.data)
	if err != nil {
		return err
	}
	tmp := ix.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, ix.path); err != nil {
		return err
	}
	ix.dirty = false
	return nil
}

// SkipDir: Synology-Systemordner (@eaDir, @tmp …, #recycle, #snapshot) und
// versteckte Ordner. Andere Namen mit „#“ oder „!“ am Anfang sind normale
// Medienordner (z.B. „#1 Kino“) und werden eingelesen.
func SkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "#recycle", "#snapshot", "lost+found":
		return true
	}
	return strings.HasPrefix(name, "@") || strings.HasPrefix(name, ".")
}

func skipDir(name string) bool { return SkipDir(name) }

// ScanResult summarises a scan.
type ScanResult struct {
	Added, Changed, Removed, Total int
	Errors                         []string
	Duration                       time.Duration
}

// Scan walks all folders: neue, geänderte und gelöschte Dateien werden erkannt.
// Fehlende Wurzelordner (z.B. nicht eingehängtes Volume) führen NICHT zum
// Löschen der Einträge.
func (ix *Index) Scan(folders []config.Folder) (ScanResult, error) {
	ix.mu.Lock()
	if ix.scanning {
		ix.mu.Unlock()
		return ScanResult{}, errors.New("Scan läuft bereits")
	}
	ix.scanning = true
	ix.mu.Unlock()
	defer func() { ix.mu.Lock(); ix.scanning = false; ix.mu.Unlock() }()

	start := time.Now()
	type found struct {
		f     config.Folder
		rel   string
		size  int64
		mtime int64
	}
	seen := map[string]found{}
	var errs []string
	reachable := map[string]bool{}
	for _, fo := range folders {
		root := filepath.Clean(fo.Path)
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			errs = append(errs, "Ordner nicht lesbar: "+root+" ("+errString(err)+") – Berechtigung für den Paketbenutzer prüfen")
			continue
		}
		reachable[root] = true
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				errs = append(errs, p+": "+err.Error())
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p != root && skipDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || !VideoExts[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			seen[p] = found{f: fo, rel: rel, size: info.Size(), mtime: info.ModTime().Unix()}
			return nil
		})
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	res := ScanResult{}
	now := time.Now().Unix()
	configured := map[string]bool{}
	for _, fo := range folders {
		configured[filepath.Clean(fo.Path)] = true
	}
	// Entfernte Dateien – auch alle Dateien aus Ordnern, die nicht mehr
	// eingetragen sind (Medienordner in der Weboberfläche entfernt).
	for id, f := range ix.data.Files {
		if _, ok := seen[f.Path]; ok {
			continue
		}
		if configured[f.Folder] && !reachable[f.Folder] {
			continue // Wurzel nicht erreichbar → nichts löschen
		}
		ix.removeFileLocked(id)
		res.Removed++
	}
	// Neue / geänderte Dateien (sortiert für stabile IDs)
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fd := seen[p]
		parsed := ParseName(fd.rel, fd.f.Type)
		if id, ok := ix.byPath[p]; ok {
			f := ix.data.Files[id]
			if it := ix.data.Items[f.ItemID]; it != nil && (it.Kind != parsed.Kind || it.LibraryID != fd.f.LibraryID || f.Folder != filepath.Clean(fd.f.Path)) {
				// Typ oder Bibliothek des Medienordners geändert (z.B. Filme →
				// Serien, Standard → eigene Bibliothek): neu einordnen.
				ix.removeFileLocked(id)
			} else {
				if f.Size != fd.size || f.MTime != fd.mtime {
					f.Size, f.MTime = fd.size, fd.mtime
					f.Probe = nil
					res.Changed++
				}
				continue
			}
		}
		file := &File{ID: ix.nextID(), Path: p, Size: fd.size, MTime: fd.mtime, Folder: filepath.Clean(fd.f.Path)}
		item := ix.findOrCreateItemLocked(parsed, fd.f, now)
		file.ItemID = item.ID
		item.FileIDs = append(item.FileIDs, file.ID)
		if item.Poster == "" || !ValidImage(item.Poster) {
			item.Poster = findPoster(p, parsed.Kind == "episode")
		}
		ix.data.Files[file.ID] = file
		ix.byPath[p] = file.ID
		res.Added++
	}
	res.Total = len(ix.data.Files)
	res.Errors = errs
	res.Duration = time.Since(start)
	ix.LastScan = time.Now()
	ix.LastErr = errs
	return res, ix.saveLocked()
}

func errString(err error) string {
	if err == nil {
		return "kein Ordner"
	}
	return err.Error()
}

func (ix *Index) removeFileLocked(id int) {
	f := ix.data.Files[id]
	delete(ix.data.Files, id)
	delete(ix.byPath, f.Path)
	it := ix.data.Items[f.ItemID]
	if it == nil {
		return
	}
	keep := it.FileIDs[:0]
	for _, fid := range it.FileIDs {
		if fid != id {
			keep = append(keep, fid)
		}
	}
	it.FileIDs = keep
	if len(it.FileIDs) == 0 {
		if k := itemKey(it); k != "" {
			delete(ix.byKey, k)
		}
		delete(ix.data.Items, it.ID)
		if it.ShowID != 0 {
			ix.gcShowLocked(it.ShowID)
		}
	}
}

func (ix *Index) gcShowLocked(showID int) {
	for _, it := range ix.data.Items {
		if it.ShowID == showID {
			return
		}
	}
	if sh := ix.data.Shows[showID]; sh != nil {
		delete(ix.byKey, showKey(sh.LibraryID, sh.Title))
	}
	delete(ix.data.Shows, showID)
}

func (ix *Index) findOrCreateItemLocked(p Parsed, fo config.Folder, now int64) *Item {
	lib := fo.LibraryID
	var it *Item
	switch p.Kind {
	case "episode":
		show := ix.findOrCreateShowLocked(p.Title, lib, now)
		it = &Item{Kind: "episode", LibraryID: lib, Title: p.EpTitle, SortTitle: SortTitle(p.EpTitle),
			Show: show.Title, ShowID: show.ID, Season: p.Season, Episode: p.Episode, EpisodeEnd: p.EpisodeEnd, AddedAt: now}
	default:
		it = &Item{Kind: p.Kind, LibraryID: lib, Title: p.Title, SortTitle: SortTitle(p.Title), Year: p.Year, AddedAt: now,
			Channel: p.Channel, Recorded: p.Recorded}
		if p.Recorded != "" && len(p.Recorded) >= 4 {
			it.Year, _ = strconv.Atoi(p.Recorded[:4])
		}
	}
	key := itemKey(it)
	if key != "" {
		if id, ok := ix.byKey[key]; ok {
			if existing := ix.data.Items[id]; existing != nil {
				return existing
			}
		}
	}
	it.ID = ix.nextID()
	ix.data.Items[it.ID] = it
	if key != "" {
		ix.byKey[key] = it.ID
	}
	return it
}

func (ix *Index) findOrCreateShowLocked(title string, lib int, now int64) *Show {
	k := showKey(lib, title)
	if id, ok := ix.byKey[k]; ok {
		if s := ix.data.Shows[id]; s != nil {
			return s
		}
	}
	s := &Show{ID: ix.nextID(), LibraryID: lib, Title: title, SortTitle: SortTitle(title), AddedAt: now}
	ix.data.Shows[s.ID] = s
	ix.byKey[k] = s.ID
	return s
}

// findPoster looks for poster images next to the file (lokale Metadaten).
func findPoster(videoPath string, episode bool) string {
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	cands := []string{base + "-poster.jpg", base + ".jpg", base + "-poster.png", base + ".png", "poster.jpg", "folder.jpg", "cover.jpg", "poster.png"}
	if episode {
		cands = append(cands, filepath.Join("..", "poster.jpg"), filepath.Join("..", "folder.jpg"))
	}
	for _, c := range cands {
		p := filepath.Join(dir, c)
		if ValidImage(p) {
			return p
		}
	}
	return ""
}

// ---------- Abfragen ----------

// Query filters items.
type Query struct {
	Kind      string
	LibraryID *int
	ShowID    int
	Title     string // Teilstring-Suche
	SortBy    string // title | added | year
	Desc      bool
	Offset    int
	Limit     int     // <=0 → alle
	Access    *Access // Sicht eines Benutzers (nil = alles)
	// WatchedBy: nur Titel, die dieses Konto angesehen hat („Zuletzt
	// gesehen“); SortBy "watched" sortiert nach dem Zeitpunkt.
	WatchedBy string
}

// lastWatchLocked: letzte Wiedergabe eines Titels durch das Konto (Unix-Zeit, 0 = nie).
func (ix *Index) lastWatchLocked(account string, it *Item) int64 {
	var last int64
	for _, fid := range it.FileIDs {
		w, ok := ix.data.Watch[account][fid]
		if !ok && account != "" {
			w, ok = ix.data.Watch[""][fid] // Positionen aus Versionen vor der Benutzererkennung
		}
		if ok && w.UpdatedAt > last {
			last = w.UpdatedAt
		}
	}
	return last
}

// Items returns copies of matching items and the total count.
func (ix *Index) Items(q Query) ([]Item, int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []Item
	needle := strings.ToLower(q.Title)
	watched := map[int]int64{}
	for _, it := range ix.data.Items {
		if q.Kind != "" && it.Kind != q.Kind {
			continue
		}
		if q.LibraryID != nil && it.LibraryID != *q.LibraryID {
			continue
		}
		if q.ShowID != 0 && it.ShowID != q.ShowID {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(it.Title+" "+it.DisplayTitle()+" "+it.Show), needle) {
			continue
		}
		if !ix.allowedLocked(q.Access, it) {
			continue
		}
		if q.WatchedBy != "" || q.SortBy == "watched" {
			last := ix.lastWatchLocked(q.WatchedBy, it)
			if q.WatchedBy != "" && last == 0 {
				continue
			}
			watched[it.ID] = last
		}
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		var less bool
		switch q.SortBy {
		case "watched":
			less = watched[a.ID] < watched[b.ID] || (watched[a.ID] == watched[b.ID] && a.ID < b.ID)
		case "added", "create_time":
			less = a.AddedAt < b.AddedAt || (a.AddedAt == b.AddedAt && a.ID < b.ID)
		case "year", "original_available":
			less = a.Year < b.Year || (a.Year == b.Year && strings.ToLower(a.SortTitle) < strings.ToLower(b.SortTitle))
		default:
			if a.Kind == "episode" && b.Kind == "episode" && (a.Season != b.Season || a.Episode != b.Episode) {
				less = a.Season < b.Season || (a.Season == b.Season && a.Episode < b.Episode)
			} else {
				less = strings.ToLower(a.SortTitle) < strings.ToLower(b.SortTitle)
			}
		}
		if q.Desc {
			return !less
		}
		return less
	})
	total := len(out)
	return page(out, q.Offset, q.Limit), total
}

func page[T any](s []T, off, lim int) []T {
	if off < 0 {
		off = 0
	}
	if off >= len(s) {
		return []T{}
	}
	s = s[off:]
	if lim > 0 && lim < len(s) {
		s = s[:lim]
	}
	return s
}

// Shows returns shows of a library.
func (ix *Index) Shows(libraryID *int, offset, limit int) ([]Show, int) {
	return ix.shows(libraryID, offset, limit, nil)
}

func (ix *Index) shows(libraryID *int, offset, limit int, a *Access) ([]Show, int) {
	return ix.showsQ(ShowQuery{LibraryID: libraryID, Offset: offset, Limit: limit}, a)
}

// ShowQuery: Serienliste mit Sortierung (title | added | date | watched).
type ShowQuery struct {
	LibraryID     *int
	Offset, Limit int
	SortBy        string
	Desc          bool
	WatchedBy     string // nur Serien mit angesehenen Folgen dieses Kontos
}

func (ix *Index) showsQ(q ShowQuery, a *Access) ([]Show, int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	// je Serie: zuletzt hinzugefügte und zuletzt gesehene (sichtbare) Folge
	added, watched := map[int]int64{}, map[int]int64{}
	if q.SortBy == "added" || q.SortBy == "watched" || q.WatchedBy != "" {
		for _, it := range ix.data.Items {
			if it.Kind != "episode" || !ix.allowedLocked(a, it) {
				continue
			}
			if it.AddedAt > added[it.ShowID] {
				added[it.ShowID] = it.AddedAt
			}
			if q.WatchedBy != "" || q.SortBy == "watched" {
				if w := ix.lastWatchLocked(q.WatchedBy, it); w > watched[it.ShowID] {
					watched[it.ShowID] = w
				}
			}
		}
	}
	var out []Show
	for _, s := range ix.data.Shows {
		if q.LibraryID != nil && s.LibraryID != *q.LibraryID {
			continue
		}
		if !ix.showAllowedLocked(a, s) {
			continue
		}
		if q.WatchedBy != "" && watched[s.ID] == 0 {
			continue
		}
		out = append(out, *s)
	}
	title := func(i, j int) bool { return strings.ToLower(out[i].SortTitle) < strings.ToLower(out[j].SortTitle) }
	key := func(sh Show) int64 {
		switch q.SortBy {
		case "added":
			if added[sh.ID] > sh.AddedAt {
				return added[sh.ID]
			}
			return sh.AddedAt
		case "watched":
			return watched[sh.ID]
		}
		return 0
	}
	sort.SliceStable(out, title)
	switch q.SortBy {
	case "added", "watched":
		sort.SliceStable(out, func(i, j int) bool {
			if q.Desc {
				return key(out[i]) > key(out[j])
			}
			return key(out[i]) < key(out[j])
		})
	case "date":
		rel := func(sh Show) string {
			if sh.Meta != nil {
				return sh.Meta.Released
			}
			return ""
		}
		sort.SliceStable(out, func(i, j int) bool {
			if q.Desc {
				return rel(out[i]) > rel(out[j])
			}
			return rel(out[i]) < rel(out[j])
		})
	default:
		if q.Desc {
			sort.SliceStable(out, func(i, j int) bool { return title(j, i) })
		}
	}
	return page(out, q.Offset, q.Limit), len(out)
}

// Item returns one item by id.
func (ix *Index) Item(id int) (Item, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	it, ok := ix.data.Items[id]
	if !ok {
		return Item{}, false
	}
	return *it, true
}

// Show returns one show by id.
func (ix *Index) Show(id int) (Show, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s, ok := ix.data.Shows[id]
	if !ok {
		return Show{}, false
	}
	return *s, true
}

// File returns one file by id.
func (ix *Index) File(id int) (File, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	f, ok := ix.data.Files[id]
	if !ok {
		return File{}, false
	}
	return *f, true
}

// Files returns the files of an item.
func (ix *Index) Files(it Item) []File {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []File
	for _, id := range it.FileIDs {
		if f, ok := ix.data.Files[id]; ok {
			out = append(out, *f)
		}
	}
	return out
}

// SetProbe stores technical data of a file.
func (ix *Index) SetProbe(fileID int, p *Probe) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if f, ok := ix.data.Files[fileID]; ok {
		f.Probe = p
		_ = ix.saveLocked()
	}
}

// SetWatch stores a playback position.
func (ix *Index) SetWatch(account string, fileID, pos int) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	m := ix.data.Watch[account]
	if m == nil {
		m = map[int]WatchState{}
		ix.data.Watch[account] = m
	}
	m[fileID] = WatchState{Position: pos, UpdatedAt: time.Now().Unix()}
	return ix.saveLocked()
}

// SetRating stores the user's own rating (1–10, 0 = löschen) for "kind:id".
func (ix *Index) SetRating(account, key string, v float64) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.data.Ratings == nil {
		ix.data.Ratings = map[string]map[string]float64{}
	}
	m := ix.data.Ratings[account]
	if v <= 0 {
		delete(m, key)
		return ix.saveLocked()
	}
	if m == nil {
		m = map[string]float64{}
		ix.data.Ratings[account] = m
	}
	m[key] = v
	return ix.saveLocked()
}

// Ratings returns a copy of the user's own ratings.
func (ix *Index) Ratings(account string) map[string]float64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make(map[string]float64, len(ix.data.Ratings[account]))
	for k, v := range ix.data.Ratings[account] {
		out[k] = v
	}
	return out
}

// Watch returns a playback position.
func (ix *Index) Watch(account string, fileID int) (WatchState, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	w, ok := ix.data.Watch[account][fileID]
	if !ok && account != "" {
		// Positionen aus Versionen vor der Benutzererkennung (Konto "").
		w, ok = ix.data.Watch[""][fileID]
	}
	return w, ok
}

// Stats for the admin UI.
func (ix *Index) Stats() map[string]any {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	kinds := map[string]int{}
	for _, it := range ix.data.Items {
		kinds[it.Kind]++
	}
	return map[string]any{"files": len(ix.data.Files), "items": kinds, "shows": len(ix.data.Shows),
		"last_scan": ix.LastScan, "scan_errors": ix.LastErr, "scanning": ix.scanning}
}

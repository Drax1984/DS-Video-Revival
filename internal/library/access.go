package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Access beschränkt, was ein Benutzer sieht: freigegebene Medienordner und
// eine FSK-Altersgrenze. nil = keine Einschränkung.
type Access struct {
	Folders      []string // erlaubte Medienordner (Pfade); nil = alle
	MaxFSK       int      // höchste erlaubte FSK; <0 = keine Grenze
	BlockUnrated bool     // Titel ohne FSK-Angabe sperren
}

// Unrestricted: alles sichtbar.
func (a *Access) Unrestricted() bool {
	return a == nil || (a.Folders == nil && a.MaxFSK < 0 && !a.BlockUnrated)
}

var fskRe = regexp.MustCompile(`(?i)^\s*(?:FSK\s*)?(0|6|12|16|18)\s*$`)

// ParseFSK: "FSK 12" → 12; unbekannt → -1.
func ParseFSK(cert string) int {
	if m := fskRe.FindStringSubmatch(cert); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1
}

// FSKLevels: Altersgruppen der FSK.
var FSKLevels = []int{0, 6, 12, 16, 18}

// fskLocked: wirksame FSK eines Titels (manuell > Metadaten; Folgen erben von
// der Serie). -1 = unbekannt.
func (ix *Index) fskLocked(it *Item) int {
	if it.FSK != nil {
		return *it.FSK
	}
	if it.Meta != nil && !it.Meta.NotFound {
		if n := ParseFSK(it.Meta.Certificate); n >= 0 {
			return n
		}
	}
	if it.Kind == "episode" {
		if sh := ix.data.Shows[it.ShowID]; sh != nil {
			return showFSK(sh)
		}
	}
	return -1
}

func showFSK(sh *Show) int {
	if sh.FSK != nil {
		return *sh.FSK
	}
	if sh.Meta != nil && !sh.Meta.NotFound {
		return ParseFSK(sh.Meta.Certificate)
	}
	return -1
}

func inFolders(p string, folders []string) bool {
	for _, f := range folders {
		f = filepath.Clean(f)
		if p == f || strings.HasPrefix(p, f+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// allowedLocked: darf der Titel gezeigt werden?
func (ix *Index) allowedLocked(a *Access, it *Item) bool {
	if a.Unrestricted() {
		return true
	}
	if a.Folders != nil {
		ok := false
		for _, fid := range it.FileIDs {
			if f := ix.data.Files[fid]; f != nil && inFolders(f.Path, a.Folders) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if a.MaxFSK >= 0 || a.BlockUnrated {
		n := ix.fskLocked(it)
		if n < 0 {
			return !a.BlockUnrated
		}
		if a.MaxFSK >= 0 && n > a.MaxFSK {
			return false
		}
	}
	return true
}

// showAllowedLocked: Serie sichtbar, wenn mindestens eine Folge sichtbar ist.
func (ix *Index) showAllowedLocked(a *Access, sh *Show) bool {
	if a.Unrestricted() {
		return true
	}
	for _, it := range ix.data.Items {
		if it.ShowID == sh.ID && it.Kind == "episode" && ix.allowedLocked(a, it) {
			return true
		}
	}
	return false
}

// SetFSK setzt die FSK eines Titels oder einer Serie von Hand (nil = automatisch).
func (ix *Index) SetFSK(kind string, id int, fsk *int) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if kind == "tvshow" {
		sh := ix.data.Shows[id]
		if sh == nil {
			return false
		}
		sh.FSK = fsk
	} else {
		it := ix.data.Items[id]
		if it == nil {
			return false
		}
		it.FSK = fsk
	}
	_ = ix.saveLocked()
	return true
}

// EffectiveFSK für die Anzeige (-1 = unbekannt) und ob sie manuell gesetzt ist.
func (ix *Index) EffectiveFSK(kind string, id int) (int, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if kind == "tvshow" {
		if sh := ix.data.Shows[id]; sh != nil {
			return showFSK(sh), sh.FSK != nil
		}
		return -1, false
	}
	if it := ix.data.Items[id]; it != nil {
		return ix.fskLocked(it), it.FSK != nil
	}
	return -1, false
}

// View: Sicht eines Benutzers auf die Bibliothek (gefiltert nach Access).
// Alle übrigen Methoden kommen unverändert vom Index.
type View struct {
	*Index
	A *Access
}

// For liefert die gefilterte Sicht.
func (ix *Index) For(a *Access) *View { return &View{Index: ix, A: a} }

func (v *View) Items(q Query) ([]Item, int) {
	q.Access = v.A
	return v.Index.Items(q)
}

func (v *View) Shows(libraryID *int, offset, limit int) ([]Show, int) {
	return v.Index.shows(libraryID, offset, limit, v.A)
}

// ShowsQ: Serien mit Sortierung bzw. nur angesehene.
func (v *View) ShowsQ(q ShowQuery) ([]Show, int) { return v.Index.showsQ(q, v.A) }

func (v *View) Item(id int) (Item, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	it, ok := v.data.Items[id]
	if !ok || !v.allowedLocked(v.A, it) {
		return Item{}, false
	}
	return *it, true
}

func (v *View) Show(id int) (Show, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	s, ok := v.data.Shows[id]
	if !ok || !v.showAllowedLocked(v.A, s) {
		return Show{}, false
	}
	return *s, true
}

func (v *View) File(id int) (File, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	f, ok := v.data.Files[id]
	if !ok {
		return File{}, false
	}
	if it := v.data.Items[f.ItemID]; it == nil || !v.allowedLocked(v.A, it) {
		return File{}, false
	}
	return *f, true
}

// FileEntry: Datei mit zugehörigem Objekt (für den Ordnerbrowser).
type FileEntry struct {
	File File
	Item Item
}

// FolderAllowed: darf der Benutzer diesen Ordner (oder einen darin) sehen?
func (v *View) FolderAllowed(p string) bool {
	if v.A == nil || v.A.Folders == nil {
		return true
	}
	return inFolders(filepath.Clean(p), v.A.Folders)
}

// FilesUnder: sichtbare, vorhandene Dateien unterhalb von dir (rekursiv).
func (v *View) FilesUnder(dir string) []FileEntry {
	dir = filepath.Clean(dir)
	v.mu.RLock()
	defer v.mu.RUnlock()
	var out []FileEntry
	for _, f := range v.data.Files {
		if f.Missing || !inFolders(f.Path, []string{dir}) || f.Path == dir {
			continue
		}
		if v.A != nil && v.A.Folders != nil && !inFolders(f.Path, v.A.Folders) {
			continue
		}
		it := v.data.Items[f.ItemID]
		if it == nil || !v.allowedLocked(v.A, it) {
			continue
		}
		out = append(out, FileEntry{File: *f, Item: *it})
	}
	return out
}

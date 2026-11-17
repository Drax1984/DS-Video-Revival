package admin

// Verwaltung einzelner Titel (nur Administratoren): Titel umbenennen und
// Videodateien löschen.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/library"
)

// setTitle: POST {kind, id, title}; leerer Titel = wieder automatisch.
// Der Titel gilt für Weboberfläche und DS-video-Apps (Anzeige und Sortierung).
func (h *Handler) setTitle(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var req struct {
		Kind  string `json:"kind"`
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t := strings.Join(strings.Fields(req.Title), " ")
	if utf8.RuneCountInString(t) > 200 {
		jsonErr(w, http.StatusBadRequest, "Titel zu lang (höchstens 200 Zeichen)")
		return
	}
	if !h.S.Lib.SetTitle(req.Kind, req.ID, t) {
		http.NotFound(w, r)
		return
	}
	var title string
	if req.Kind == "tvshow" {
		sh, _ := h.S.Lib.Show(req.ID)
		title = sh.DisplayTitle()
	} else {
		it, _ := h.S.Lib.Item(req.ID)
		title = it.DisplayTitle()
		if it.Kind == "episode" {
			title = it.EpisodeTitle()
		}
	}
	if t == "" {
		log.Printf("admin: Titel von %s %d zurückgesetzt auf „%s“ (%s)", req.Kind, req.ID, title, sess.Account)
	} else {
		log.Printf("admin: Titel von %s %d geändert auf „%s“ (%s)", req.Kind, req.ID, t, sess.Account)
	}
	writeJSON(w, map[string]any{"ok": true, "title": title, "custom_title": t})
}

// recycleDir: Papierkorb des freigegebenen Ordners (/volumeX/share/#recycle),
// sofort nutzbar nur, wenn er in DSM aktiviert ist (Ordner vorhanden).
func recycleDir(p string) (string, string, bool) {
	vol, share, _, ok := splitShare(filepath.Clean(p))
	if !ok {
		return "", "", false
	}
	root := filepath.Join(vol, share)
	rc := filepath.Join(root, "#recycle")
	if st, err := os.Stat(rc); err == nil && st.IsDir() {
		return rc, root, true
	}
	return "", root, false
}

func recycleAvailable(p string) bool {
	if p == "" {
		return false
	}
	_, _, ok := recycleDir(p)
	return ok
}

func (h *Handler) firstFilePath(it library.Item) string {
	for _, id := range it.FileIDs {
		if f, ok := h.S.Lib.File(id); ok {
			return f.Path
		}
	}
	return ""
}

// canWrite: darf der Paketbenutzer in dir schreiben? Echter Schreibversuch
// (kurzlebige versteckte Datei), weil Synology-ACLs von den Modusbits abweichen.
func canWrite(dir string) bool {
	f, err := os.CreateTemp(dir, ".dsvr-schreibtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// moveToRecycle verschiebt p in den Papierkorb der Freigabe (gleicher
// relativer Pfad wie bei DSM); vorhandene Namen bekommen einen Zusatz.
func moveToRecycle(p, rc, root string) error {
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("Pfad außerhalb der Freigabe")
	}
	dst := filepath.Join(rc, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		ext := filepath.Ext(dst)
		base := strings.TrimSuffix(dst, ext)
		for i := 1; ; i++ {
			c := fmt.Sprintf("%s (%d)%s", base, i, ext)
			if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) {
				dst = c
				break
			}
		}
	}
	return os.Rename(p, dst)
}

// deleteFile: POST {file_id} löscht genau eine Videodatei (Ordneransicht,
// z.B. einen Doppelgänger) – nur DSM-Administratoren. Gleiches Verfahren wie
// deleteItem (Papierkorb, need_write).
func (h *Handler) deleteFile(w http.ResponseWriter, r *http.Request, sess auth.Session, sid string) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if !h.S.Auth.IsAdmin(sess) {
		jsonErr(w, http.StatusForbidden, "Nur für DSM-Administratoren")
		return
	}
	var req struct {
		FileID int `json:"file_id"`
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
	title := filepath.Base(f.Path)
	kind := ""
	if it, ok := h.S.Lib.Item(f.ItemID); ok {
		title, kind = it.DisplayTitle(), it.Kind
	}
	h.removeFiles(w, r, sess, sid, []library.File{f}, title, kind)
}

// deleteItem: POST {kind, id} löscht alle Videodateien eines Films/einer
// Episode/eines Heimvideos – in den Papierkorb der Freigabe, wenn dieser
// aktiv ist, sonst endgültig. Fehlt dem Paketbenutzer das Schreibrecht,
// meldet die Antwort need_write (die Oberfläche fragt dann nach).
func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request, sess auth.Session, sid string) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var req struct {
		Kind string `json:"kind"`
		ID   int    `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Kind == "tvshow" {
		jsonErr(w, http.StatusBadRequest, "Serien bitte folgenweise löschen")
		return
	}
	it, ok := h.S.Lib.Item(req.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var files []library.File
	for _, id := range it.FileIDs {
		if f, ok := h.S.Lib.File(id); ok {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		jsonErr(w, http.StatusNotFound, "keine Dateien")
		return
	}
	h.removeFiles(w, r, sess, sid, files, it.DisplayTitle(), it.Kind)
}

// removeFiles: Dateien in den Papierkorb der Freigabe (falls aktiv) bzw.
// endgültig löschen und aus der Bibliothek nehmen; Antwort an die Oberfläche.
//
// Darf der Paketbenutzer im Ordner nicht schreiben (z.B. von einem
// Download-Programm angelegte Unterordner mit eigenen Linux-Rechten), wird die
// Datei mit der DSM-Sitzung des angemeldeten Administrators über die File
// Station gelöscht (SYNO.FileStation.Delete, Methode delete) – mit genau den
// Rechten, die er auch in der File Station hätte. Ohne DSM-Sitzung (lokale
// Anmeldung) bleibt es bei need_write (Oberfläche bietet Schreibrecht an).
func (h *Handler) removeFiles(w http.ResponseWriter, r *http.Request, sess auth.Session, sid string, files []library.File, title, kind string) {
	viaDSM := sid != "" && h.S.Cfg().AuthMode != "local"
	needWrite := func(f library.File) map[string]any {
		_, share, _, _ := splitShare(f.Path)
		return map[string]any{"ok": false, "need_write": true, "path": filepath.Dir(f.Path), "share": share,
			"message": fmt.Sprintf("Der Paketbenutzer „%s“ hat kein Schreibrecht auf „%s“.", packageUser(), share)}
	}
	if !viaDSM {
		// Vorab prüfen, damit nicht nur ein Teil gelöscht wird.
		for _, f := range files {
			if !canWrite(filepath.Dir(f.Path)) {
				writeJSON(w, needWrite(f))
				return
			}
		}
	}
	var done []int
	recycled, byDSM := 0, 0
	fail := func(f library.File, err error) {
		log.Printf("admin: „%s“: %s nicht gelöscht: %v", title, f.Path, err)
		h.S.Lib.RemoveFiles(done)
		msg := "Löschen fehlgeschlagen: " + err.Error()
		if errors.Is(err, fs.ErrPermission) {
			msg = fmt.Sprintf("Kein Schreibrecht für %s.", f.Path)
		}
		writeJSON(w, map[string]any{"ok": false, "message": msg, "deleted": len(done)})
	}
	for _, f := range files {
		how := "endgültig gelöscht"
		if !canWrite(filepath.Dir(f.Path)) {
			// nur mit DSM-Sitzung erreichbar (sonst oben abgefangen)
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
			err := h.dsmDelete(ctx, sid, f.Path)
			cancel()
			if err != nil {
				if !canWrite(shareRoot(f.Path)) && len(done) == 0 {
					// Freigabe ganz ohne Schreibrecht: Oberfläche bietet es an.
					log.Printf("admin: %s über DSM nicht gelöscht (%v) – frage nach Schreibrecht", f.Path, err)
					writeJSON(w, needWrite(f))
					return
				}
				fail(f, fmt.Errorf("auch über DSM (File Station, Konto %s) nicht möglich: %v – bitte in der File Station prüfen", sess.Account, err))
				return
			}
			if _, err := os.Stat(f.Path); err == nil {
				fail(f, fmt.Errorf("DSM meldet Erfolg, die Datei ist aber noch vorhanden"))
				return
			}
			byDSM++
			how = "über DSM (File Station) gelöscht"
		} else {
			var err error
			if rc, root, ok := recycleDir(f.Path); ok {
				if err = moveToRecycle(f.Path, rc, root); err == nil {
					recycled++
					how = "in den Papierkorb verschoben"
				}
			} else {
				err = os.Remove(f.Path)
			}
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				fail(f, err)
				return
			}
		}
		done = append(done, f.ID)
		log.Printf("admin: %s %s (%s, „%s“, von %s)", f.Path, how, kind, title, sess.Account)
	}
	h.S.Lib.RemoveFiles(done)
	msg := fmt.Sprintf("„%s“ gelöscht (%d Datei(en) endgültig).", title, len(done))
	switch {
	case byDSM > 0:
		msg = fmt.Sprintf("„%s“ gelöscht (%d Datei(en), davon %d über DSM wie in der File Station – Papierkorb, falls für die Freigabe aktiv).", title, len(done), byDSM)
	case recycled == len(done):
		msg = fmt.Sprintf("„%s“ in den Papierkorb verschoben (%d Datei(en)).", title, len(done))
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg, "deleted": len(done), "recycled": recycled, "via_dsm": byDSM})
}

// dsmDelete löscht eine Datei über die File Station API von DSM
// (SYNO.FileStation.Delete v2, Methode delete – blockierend; Pfad beginnt mit
// dem freigegebenen Ordner, z.B. "/Downloads/…"). recursive=false: niemals
// Ordnerinhalte.
func (h *Handler) dsmDelete(ctx context.Context, sid, p string) error {
	_, _, sp, ok := splitShare(filepath.Clean(p)) // sp: /freigabe/…
	if !ok {
		return fmt.Errorf("Pfad liegt in keinem freigegebenen Ordner")
	}
	b, _ := json.Marshal(sp)
	_, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.FileStation.Delete"}, "version": {"2"}, "method": {"delete"},
		"path": {string(b)}, "recursive": {"false"}})
	return err
}

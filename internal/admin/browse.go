package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"dsvideorevival/internal/library"
)

// Ordnerauswahl und Leserecht für den Paketbenutzer.
//
// Auflisten: SYNO.FileStation.List (list_share / list, offizieller File
// Station API Guide) mit der DSM-Sitzung des angemeldeten Administrators –
// so erscheinen auch Ordner, die der Paketbenutzer noch nicht lesen darf.
// Ohne DSM-Sitzung (lokaler Modus) oder bei Fehlern: Dateisystem direkt.
//
// Leserecht: SYNO.Core.Share.Permission (list/set, user_group_type
// „system“ = „Systeminterner Benutzer“). Diese API ist nicht offiziell
// dokumentiert (Belege: N4S4/synology-api core_share.py). Deshalb wird nur
// der Eintrag des Paketbenutzers gesetzt, die übrigen Einträge werden vorher
// gesichert, hinterher verglichen und bei Abweichung zurückgeschrieben, und
// das Ergebnis wird durch einen echten Lesezugriff geprüft.

type dirEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"` // echter Pfad, z.B. /volume1/video/Filme
	Readable bool   `json:"readable"`
}

var volRe = regexp.MustCompile(`^/volume[0-9A-Za-z_]+$`)

func packageUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "dsvideorevival"
}

func canRead(p string) bool {
	_, err := os.ReadDir(p)
	return err == nil
}

// splitShare: /volume1/video/Filme → ("/volume1", "video", "/video/Filme").
func splitShare(p string) (vol, share, fsPath string, ok bool) {
	p = filepath.Clean(p)
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) < 2 || !volRe.MatchString("/"+parts[0]) || parts[1] == "" || strings.HasPrefix(parts[1], "@") {
		return "", "", "", false
	}
	return "/" + parts[0], parts[1], "/" + strings.Join(parts[1:], "/"), true
}

func hiddenDir(name string) bool { return library.SkipDir(name) }

func (h *Handler) browse(w http.ResponseWriter, r *http.Request, sid string) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var dirs []dirEntry
	var err error
	if p == "" || p == "/" {
		dirs, err = h.listShares(ctx, sid)
	} else {
		if _, _, _, ok := splitShare(p); !ok {
			http.Error(w, "ungültiger Pfad", http.StatusBadRequest)
			return
		}
		dirs, err = h.listDirs(ctx, sid, filepath.Clean(p))
	}
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	res := map[string]any{"path": p, "dirs": dirs, "user": packageUser()}
	if p != "" && p != "/" {
		res["readable"] = canRead(p)
		parent := filepath.Dir(filepath.Clean(p))
		if _, _, _, ok := splitShare(parent); !ok {
			parent = ""
		}
		res["parent"] = parent
	}
	writeJSON(w, res)
}

func (h *Handler) listShares(ctx context.Context, sid string) ([]dirEntry, error) {
	if sid != "" && h.S.Cfg().AuthMode != "local" {
		m, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.FileStation.List"}, "version": {"2"}, "method": {"list_share"},
			"additional": {"real_path"}, "limit": {"1000"}})
		if err == nil {
			d, _ := m["data"].(map[string]any)
			list, _ := d["shares"].([]any)
			var out []dirEntry
			for _, x := range list {
				s, _ := x.(map[string]any)
				name, _ := s["name"].(string)
				add, _ := s["additional"].(map[string]any)
				rp, _ := add["real_path"].(string)
				if name == "" || rp == "" {
					continue
				}
				out = append(out, dirEntry{Name: name, Path: rp, Readable: canRead(rp)})
			}
			if len(out) > 0 {
				return out, nil
			}
		} else {
			log.Printf("admin: Ordnerliste über DSM nicht möglich (%v) – lese Dateisystem", err)
		}
	}
	vols, _ := filepath.Glob("/volume*")
	var out []dirEntry
	for _, v := range vols {
		if !volRe.MatchString(v) {
			continue
		}
		ents, err := os.ReadDir(v)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() && !hiddenDir(e.Name()) {
				p := filepath.Join(v, e.Name())
				out = append(out, dirEntry{Name: e.Name() + " (" + strings.TrimPrefix(v, "/") + ")", Path: p, Readable: canRead(p)})
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("keine freigegebenen Ordner gefunden")
	}
	return out, nil
}

func (h *Handler) listDirs(ctx context.Context, sid, p string) ([]dirEntry, error) {
	vol, _, fsPath, _ := splitShare(p)
	if ents, err := os.ReadDir(p); err == nil {
		var out []dirEntry
		for _, e := range ents {
			if e.IsDir() && !hiddenDir(e.Name()) {
				out = append(out, dirEntry{Name: e.Name(), Path: filepath.Join(p, e.Name()), Readable: true})
			}
		}
		return out, nil
	}
	if sid == "" || h.S.Cfg().AuthMode == "local" {
		return nil, fmt.Errorf("Ordner nicht lesbar – erst Leserecht setzen")
	}
	m, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.FileStation.List"}, "version": {"2"}, "method": {"list"},
		"folder_path": {fsPath}, "filetype": {"dir"}, "additional": {"real_path"}, "limit": {"2000"}})
	if err != nil {
		return nil, err
	}
	d, _ := m["data"].(map[string]any)
	list, _ := d["files"].([]any)
	var out []dirEntry
	for _, x := range list {
		f, _ := x.(map[string]any)
		name, _ := f["name"].(string)
		if isDir, _ := f["isdir"].(bool); !isDir || name == "" || hiddenDir(name) {
			continue
		}
		rp := filepath.Join(p, name)
		if add, _ := f["additional"].(map[string]any); add != nil {
			if s, _ := add["real_path"].(string); strings.HasPrefix(s, vol+"/") {
				rp = s
			}
		}
		out = append(out, dirEntry{Name: name, Path: rp, Readable: false})
	}
	return out, nil
}

// grant: Leserecht des Paketbenutzers auf den freigegebenen Ordner von path.
func (h *Handler) grant(w http.ResponseWriter, r *http.Request, sid string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path  string `json:"path"`
		Write bool   `json:"write"` // Lesen/Schreiben (zum Löschen) statt nur Lesen
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := filepath.Clean(req.Path)
	_, share, _, ok := splitShare(p)
	if !ok {
		http.Error(w, "ungültiger Pfad", http.StatusBadRequest)
		return
	}
	pu := packageUser()
	right, check := "Leserecht", canRead
	manual := fmt.Sprintf("Bitte von Hand: Systemsteuerung → Freigegebener Ordner → „%s“ → Bearbeiten → Berechtigungen → „Systeminterner Benutzer“ → %s: Lesen.", share, pu)
	if req.Write {
		right, check = "Schreibrecht", canWrite
		manual = fmt.Sprintf("Bitte von Hand: Systemsteuerung → Freigegebener Ordner → „%s“ → Bearbeiten → Berechtigungen → „Systeminterner Benutzer“ → %s: Lesen/Schreiben.", share, pu)
	}
	if check(p) {
		writeJSON(w, map[string]any{"ok": true, "message": right + " ist bereits vorhanden."})
		return
	}
	if sid == "" || h.S.Cfg().AuthMode == "local" {
		writeJSON(w, map[string]any{"ok": false, "message": "Ohne DSM-Anmeldung nicht möglich. " + manual})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := h.grantPerm(ctx, sid, share, pu, req.Write); err != nil {
		log.Printf("admin: %s für %s auf „%s“ nicht gesetzt: %v", right, pu, share, err)
		writeJSON(w, map[string]any{"ok": false, "message": right + " konnte nicht gesetzt werden (" + err.Error() + "). " + manual})
		return
	}
	// DSM übernimmt die Rechte nicht immer sofort.
	for i := 0; i < 20 && !check(p); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !check(p) {
		log.Printf("admin: %s für %s auf „%s“ gesetzt, Ordner %s aber noch ohne Zugriff", right, pu, share, p)
		writeJSON(w, map[string]any{"ok": false, "message": right + " gesetzt, der Ordner ist aber noch nicht zugänglich (evtl. abweichende Rechte in Unterordnern). " + manual})
		return
	}
	log.Printf("admin: %s für %s auf „%s“ gesetzt", right, pu, share)
	writeJSON(w, map[string]any{"ok": true, "message": right + " auf „" + share + "“ gesetzt."})
}

type sharePerm map[string]any

func (h *Handler) sharePerms(ctx context.Context, sid, share string) ([]sharePerm, error) {
	m, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.Core.Share.Permission"}, "version": {"1"}, "method": {"list"},
		"name": {share}, "user_group_type": {"system"}, "action": {"enum"}, "offset": {"0"}, "limit": {"500"},
		"is_unite_permission": {"false"}, "with_inherit": {"false"}})
	if err != nil {
		return nil, err
	}
	d, _ := m["data"].(map[string]any)
	items, _ := d["items"].([]any)
	var out []sharePerm
	for _, x := range items {
		if p, ok := x.(map[string]any); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func permKey(p sharePerm) string {
	b, _ := json.Marshal(map[string]any{"r": p["is_readonly"], "w": p["is_writable"], "d": p["is_deny"], "c": p["is_custom"]})
	return string(b)
}

func (h *Handler) setPerms(ctx context.Context, sid, share string, perms []sharePerm) error {
	b, _ := json.Marshal(perms)
	_, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.Core.Share.Permission"}, "version": {"1"}, "method": {"set"},
		"name": {share}, "user_group_type": {"system"}, "permissions": {string(b)}})
	return err
}

func (h *Handler) grantPerm(ctx context.Context, sid, share, pu string, write bool) error {
	before, err := h.sharePerms(ctx, sid, share)
	if err != nil {
		return err
	}
	found := false
	for _, p := range before {
		if p["name"] == pu {
			found = true
			if d, _ := p["is_deny"].(bool); d {
				return fmt.Errorf("für %s ist „Kein Zugriff“ gesetzt", pu)
			}
		}
	}
	if !found {
		return fmt.Errorf("Systembenutzer %s in den Rechten von „%s“ nicht gefunden", pu, share)
	}
	own := sharePerm{"name": pu, "is_readonly": !write, "is_writable": write, "is_deny": false, "is_custom": false}
	err = h.setPerms(ctx, sid, share, []sharePerm{own})
	if err != nil {
		return err
	}
	// Gegenprobe: andere Einträge unverändert? Sonst zurückschreiben.
	after, err := h.sharePerms(ctx, sid, share)
	if err != nil {
		return nil // gesetzt; Lesetest entscheidet
	}
	now := map[string]string{}
	for _, p := range after {
		if n, _ := p["name"].(string); n != "" {
			now[n] = permKey(p)
		}
	}
	changed := 0
	full := []sharePerm{own}
	for _, p := range before {
		n, _ := p["name"].(string)
		if n == "" || n == pu {
			continue
		}
		if now[n] != permKey(p) {
			changed++
		}
		full = append(full, sharePerm{"name": n, "is_readonly": p["is_readonly"], "is_writable": p["is_writable"], "is_deny": p["is_deny"], "is_custom": p["is_custom"]})
	}
	if changed > 0 {
		// Vollständige Liste zurückschreiben (eigener Eintrag mit neuem Recht).
		log.Printf("admin: %d andere Rechteeinträge auf „%s“ verändert – stelle sie wieder her", changed, share)
		if err := h.setPerms(ctx, sid, share, full); err != nil {
			return fmt.Errorf("andere Rechte verändert und Wiederherstellung fehlgeschlagen: %v", err)
		}
	}
	return nil
}

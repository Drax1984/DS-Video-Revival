package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/vs"
)

// Benutzerrechte: Freigaben je Medienordner und Jugendschutz (FSK).
// Wer sich überhaupt anmelden darf, regelt DSM (Anwendungsberechtigungen).

type accessFolder struct {
	Path  string   `json:"path"`
	Type  string   `json:"type"`
	Allow []string `json:"allow"`
}

type accessView struct {
	Folders       []accessFolder                `json:"folders"`
	Restrictions  map[string]config.Restriction `json:"restrictions"`
	VGroups       map[string][]string           `json:"vgroups"`
	Users         []string                      `json:"users"`
	Groups        []string                      `json:"groups"`
	AdminGroup    string                        `json:"admin_group"`
	DownloadAllow []string                      `json:"download_allow"`
	SingleSession []string                      `json:"single_session"`
	SingleIdle    int                           `json:"single_session_idle"` // Minuten
	Levels        []int                         `json:"levels"`
	Source        string                        `json:"source"` // dsm | system
}

func (h *Handler) access(w http.ResponseWriter, r *http.Request, sid string) {
	c := *h.S.Cfg()
	switch r.Method {
	case http.MethodGet:
		v := accessView{Restrictions: c.Restrictions, VGroups: c.Groups, Levels: library.FSKLevels}
		if v.VGroups == nil {
			v.VGroups = map[string][]string{}
		}
		if v.Restrictions == nil {
			v.Restrictions = map[string]config.Restriction{}
		}
		for _, f := range c.Folders {
			al := f.Allow
			if al == nil {
				al = []string{}
			}
			v.Folders = append(v.Folders, accessFolder{f.Path, f.Type, al})
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		v.Users, v.Groups, v.Source = h.principals(ctx, sid)
		if v.Users == nil {
			v.Users = []string{}
		}
		if v.Groups == nil {
			v.Groups = []string{}
		}
		v.VGroups = copyGroups(v.VGroups)
		if _, ok := v.VGroups[config.AdminGroup]; !ok {
			v.VGroups[config.AdminGroup] = []string{}
		}
		v.AdminGroup = config.AdminGroup
		v.DownloadAllow = append([]string{}, c.DownloadAllow...)
		v.SingleSession = append([]string{}, c.SingleSession...)
		v.SingleIdle = int(h.S.ConnIdle().Minutes())
		writeJSON(w, v)
	case http.MethodPut:
		var in struct {
			Allow        map[string][]string           `json:"allow"`
			Restrictions map[string]config.Restriction `json:"restrictions"`
			Groups       map[string][]string           `json:"groups"`
			// nil = unverändert (ältere Oberfläche), [] = alle
			DownloadAllow *[]string `json:"download_allow"`
			SingleSession *[]string `json:"single_session"` // nil = unverändert, [] = niemand
			SingleIdle    *int      `json:"single_session_idle"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		groups := map[string][]string{}
		if _, ok := in.Groups[config.AdminGroup]; !ok {
			// Administratorengruppe lässt sich nicht löschen/umbenennen.
			if in.Groups == nil {
				in.Groups = map[string][]string{}
			}
			in.Groups[config.AdminGroup] = c.Groups[config.AdminGroup]
		}
		for name, members := range in.Groups {
			name = strings.TrimSpace(name)
			if name == "" || strings.ContainsAny(name, ":\n\r") || len(name) > 64 {
				jsonErr(w, http.StatusBadRequest, "ungültiger Gruppenname: "+name)
				return
			}
			var ms []string
			for _, p := range cleanPrincipals(prefixAll("user:", members)) {
				ms = append(ms, strings.TrimPrefix(p, "user:"))
			}
			groups[strings.ToLower(name)] = ms
		}
		exists := func(p string) bool {
			if g, ok := strings.CutPrefix(p, "vgroup:"); ok {
				_, have := groups[g]
				return have
			}
			return true
		}
		for i, f := range c.Folders {
			var al []string
			for _, p := range cleanPrincipals(in.Allow[f.Path]) {
				if exists(p) { // gelöschte Gruppen entfallen
					al = append(al, p)
				}
			}
			c.Folders[i].Allow = al
		}
		if in.DownloadAllow != nil {
			var dl []string
			for _, p := range cleanPrincipals(*in.DownloadAllow) {
				if exists(p) && p != "vgroup:"+config.AdminGroup {
					dl = append(dl, p)
				}
			}
			c.DownloadAllow = dl
		} else {
			var dl []string
			for _, p := range c.DownloadAllow {
				if exists(strings.ToLower(p)) { // gelöschte Gruppen entfallen
					dl = append(dl, p)
				}
			}
			c.DownloadAllow = dl
		}
		var ss []string
		if in.SingleSession != nil {
			for _, p := range cleanPrincipals(*in.SingleSession) {
				if exists(p) && p != "vgroup:"+config.AdminGroup {
					ss = append(ss, p)
				}
			}
		} else {
			for _, p := range c.SingleSession {
				if exists(strings.ToLower(p)) {
					ss = append(ss, p)
				}
			}
		}
		c.SingleSession = ss
		if in.SingleIdle != nil {
			if *in.SingleIdle < 1 || *in.SingleIdle > 240 {
				jsonErr(w, http.StatusBadRequest, "Ruhezeit: 1 bis 240 Minuten")
				return
			}
			c.SingleSessionIdle = *in.SingleIdle
			if c.SingleSessionIdle == int(vs.ConnIdle.Minutes()) {
				c.SingleSessionIdle = 0 // Standard
			}
		}
		c.Groups = groups
		if len(groups) == 0 {
			c.Groups = nil
		}
		res := map[string]config.Restriction{}
		for k, r := range in.Restrictions {
			k = strings.TrimSpace(k)
			if !exists(strings.ToLower(k)) {
				continue // Regel einer gelöschten Gruppe
			}
			if len(cleanPrincipals([]string{k})) != 1 {
				jsonErr(w, http.StatusBadRequest, "ungültiger Eintrag: "+k)
				return
			}
			ok := false
			for _, l := range library.FSKLevels {
				ok = ok || r.MaxFSK == l
			}
			if !ok {
				jsonErr(w, http.StatusBadRequest, "FSK muss 0, 6, 12, 16 oder 18 sein")
				return
			}
			res[strings.ToLower(k)] = r
		}
		c.Restrictions = res
		if len(res) == 0 {
			c.Restrictions = nil
		}
		if err := config.Save(h.CfgPath, c); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		before := strings.Join(h.S.Cfg().Groups[config.AdminGroup], ", ")
		h.S.SetCfg(c)
		dlWho := "alle"
		if len(c.DownloadAllow) > 0 {
			dlWho = strings.Join(c.DownloadAllow, ", ")
		}
		ssWho := "niemand"
		if len(c.SingleSession) > 0 {
			ssWho = strings.Join(c.SingleSession, ", ")
		}
		log.Printf("admin: Benutzerrechte geändert (%d Jugendschutz-Regeln, Herunterladen: %s, nur eine Verbindung: %s, Ruhezeit %d Min.)", len(res), dlWho, ssWho, int(h.S.ConnIdle().Minutes()))
		if after := strings.Join(c.Groups[config.AdminGroup], ", "); after != before {
			log.Printf("admin: Administratoren von DS Video Revival jetzt: %s (vorher: %s)", orNone(after), orNone(before))
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "GET/PUT")
	}
}

func orNone(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func copyGroups(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in)+1)
	for k, v := range in {
		out[k] = append([]string{}, v...)
	}
	return out
}

func cleanPrincipals(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		kind, name, ok := strings.Cut(p, ":")
		if !ok || (kind != "user" && kind != "group" && kind != "vgroup") || name == "" || strings.ContainsAny(name, ":\n\r") || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// principals: lokale DSM-Benutzer und -Gruppen zur Auswahl.
// DSM: SYNO.Core.User/Group list (type=local; vgl. N4S4/synology-api) mit der
// Sitzung des Administrators; sonst /etc/passwd und /etc/group.
func (h *Handler) principals(ctx context.Context, sid string) (users, groups []string, src string) {
	if sid != "" && h.S.Cfg().AuthMode != "local" {
		get := func(api, key string) []string {
			m, err := h.dsmCall(ctx, sid, url.Values{"api": {api}, "version": {"1"}, "method": {"list"},
				"type": {"local"}, "offset": {"0"}, "limit": {"-1"}})
			if err != nil {
				return nil
			}
			d, _ := m["data"].(map[string]any)
			list, _ := d[key].([]any)
			var out []string
			for _, x := range list {
				if o, _ := x.(map[string]any); o != nil {
					if n, _ := o["name"].(string); n != "" {
						out = append(out, n)
					}
				}
			}
			return out
		}
		users, groups = get("SYNO.Core.User", "users"), get("SYNO.Core.Group", "groups")
		if len(users) > 0 || len(groups) > 0 {
			src = "dsm"
		}
	}
	if src == "" && h.S.Cfg().AuthMode == "local" && h.S.Auth != nil && h.S.Auth.Local != nil {
		src = "local"
		users = h.S.Auth.Local.Names()
	}
	if src == "" {
		src = "system"
		users = readIDFile("/etc/passwd", 1024, 1<<30)
		groups = readIDFile("/etc/group", 100, 101)
		groups = append(groups, readIDFile("/etc/group", 65536, 1<<30)...)
	}
	var g2 []string
	for _, g := range groups {
		if g != "http" {
			g2 = append(g2, g)
		}
	}
	sort.Strings(users)
	sort.Strings(g2)
	return users, g2, src
}

// readIDFile: Namen aus passwd/group mit ID im Bereich [lo, hi].
func readIDFile(path string, lo, hi int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) < 3 {
			continue
		}
		if id, err := strconv.Atoi(p[2]); err == nil && id >= lo && id <= hi && p[0] != "" {
			out = append(out, p[0])
		}
	}
	return out
}

// fsk: FSK eines Titels von Hand setzen (nil = aus den Metadaten).
func (h *Handler) fsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var req struct {
		Kind string `json:"kind"`
		ID   int    `json:"id"`
		FSK  *int   `json:"fsk"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.FSK != nil {
		ok := false
		for _, l := range library.FSKLevels {
			ok = ok || *req.FSK == l
		}
		if !ok {
			jsonErr(w, http.StatusBadRequest, "FSK muss 0, 6, 12, 16 oder 18 sein")
			return
		}
	}
	if !h.S.Lib.SetFSK(req.Kind, req.ID, req.FSK) {
		http.NotFound(w, r)
		return
	}
	n, manual := h.S.Lib.EffectiveFSK(req.Kind, req.ID)
	writeJSON(w, map[string]any{"ok": true, "fsk": n, "fsk_manual": manual})
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"error": msg})
}

func prefixAll(pre string, in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, pre+strings.TrimSpace(s))
	}
	return out
}

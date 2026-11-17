package vs

import (
	"bufio"
	"os"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
)

// Inhaltsfreigaben (Weboberfläche → Medienordner/Benutzerrechte):
//   - Folder.Allow: wer einen Medienordner sehen darf ("user:x", "group:y";
//     leer = alle).
//   - Config.Restrictions: FSK-Grenze je Benutzer oder Gruppe. Eine Regel für
//     den Benutzer selbst geht vor; sonst gilt die strengste seiner Gruppen.
// Gruppenmitgliedschaft: /etc/group der DSM (lokale Gruppen); jeder lokale
// DSM-Benutzer gehört außerdem zur Gruppe „users“.
// Unbekanntes Konto bei aktiven Regeln: strengste Einstellung (sicher für Kinder).

// GroupFile ist für Tests austauschbar.
var GroupFile = "/etc/group"

var grpCache struct {
	sync.Mutex
	at  time.Time
	mem map[string][]string // user → Gruppen
}

func loadGroupsLocked() {
	if grpCache.mem != nil && time.Since(grpCache.at) <= time.Minute {
		return
	}
	grpCache.mem = map[string][]string{}
	grpCache.at = time.Now()
	f, err := os.Open(GroupFile)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) < 4 || p[0] == "" || strings.HasPrefix(p[0], "#") {
			continue
		}
		for _, m := range strings.Split(p[3], ",") {
			if m = strings.ToLower(strings.TrimSpace(m)); m != "" {
				grpCache.mem[m] = append(grpCache.mem[m], p[0])
			}
		}
	}
}

// UserGroups liefert die lokalen DSM-Gruppen eines Benutzers.
func UserGroups(user string) []string {
	grpCache.Lock()
	defer grpCache.Unlock()
	loadGroupsLocked()
	return append([]string{"users"}, grpCache.mem[strings.ToLower(user)]...)
}

// IsAppAdmin: Administrator in DS Video Revival (Weboberfläche und Apps) –
// DSM-Administratoren immer (Aussperrschutz), dazu die Mitglieder der eigenen
// Gruppe „administrators“. Gibt keinerlei Rechte in DSM.
func (s *Server) IsAppAdmin(sess auth.Session) bool {
	if s.Auth != nil && s.Auth.IsAdmin(sess) {
		return true
	}
	if !knownAccount(sess.Account) {
		return false
	}
	for _, m := range s.Cfg().Groups[config.AdminGroup] {
		if strings.EqualFold(m, sess.Account) {
			return true
		}
	}
	return false
}

func knownAccount(a string) bool { return a != "" && a != "?" }

// principalKeys: Einträge, die auf das Konto passen ("user:x", "group:y", "vgroup:z").
func principalKeys(cfg *config.Config, user string) map[string]bool {
	keys := map[string]bool{}
	if !knownAccount(user) {
		return keys
	}
	keys["user:"+user] = true
	for _, g := range UserGroups(user) {
		keys["group:"+strings.ToLower(g)] = true
	}
	for name, members := range cfg.Groups {
		for _, m := range members {
			if strings.EqualFold(m, user) {
				keys["vgroup:"+strings.ToLower(name)] = true
			}
		}
	}
	return keys
}

// MayDownload: darf die Sitzung in den Apps herunterladen (Download und
// Offline-Umwandlung)? Leere Liste = alle; Administratoren immer.
func (s *Server) MayDownload(sess auth.Session) bool {
	cfg := s.Cfg()
	if len(cfg.DownloadAllow) == 0 || s.IsAppAdmin(sess) {
		return true
	}
	keys := principalKeys(cfg, strings.ToLower(sess.Account))
	for _, k := range cfg.DownloadAllow {
		if keys[strings.ToLower(k)] {
			return true
		}
	}
	return false
}

// AccessFor: was darf die Sitzung sehen?
func (s *Server) AccessFor(sess auth.Session) *library.Access {
	if s.ConnBlocked(sess) {
		a := denyAll
		return &a
	}
	cfg := s.Cfg()
	restricted := len(cfg.Restrictions) > 0
	for _, f := range cfg.Folders {
		if len(f.Allow) > 0 {
			restricted = true
		}
	}
	if !restricted {
		return nil
	}
	// Aussperrschutz: Administratoren (DSM und DS Video Revival) sehen alles.
	if s.IsAppAdmin(sess) {
		return nil
	}
	user := strings.ToLower(sess.Account)
	keys := principalKeys(cfg, user)
	a := &library.Access{MaxFSK: -1, Folders: []string{}}
	for _, f := range cfg.Folders {
		if len(f.Allow) == 0 {
			a.Folders = append(a.Folders, f.Path)
			continue
		}
		for _, k := range f.Allow {
			if keys[strings.ToLower(k)] {
				a.Folders = append(a.Folders, f.Path)
				break
			}
		}
	}
	if !knownAccount(user) {
		// Konto unbekannt: strengste Regel.
		for _, r := range cfg.Restrictions {
			if a.MaxFSK < 0 || r.MaxFSK < a.MaxFSK {
				a.MaxFSK = r.MaxFSK
			}
			a.BlockUnrated = true
		}
		return a
	}
	if r, ok := cfg.Restrictions["user:"+user]; ok {
		a.MaxFSK, a.BlockUnrated = r.MaxFSK, r.BlockUnrated
		return a
	}
	for k, r := range cfg.Restrictions {
		lk := strings.ToLower(k)
		if !keys[lk] || !(strings.HasPrefix(lk, "group:") || strings.HasPrefix(lk, "vgroup:")) {
			continue
		}
		if a.MaxFSK < 0 || r.MaxFSK < a.MaxFSK {
			a.MaxFSK = r.MaxFSK
		}
		a.BlockUnrated = a.BlockUnrated || r.BlockUnrated
	}
	return a
}

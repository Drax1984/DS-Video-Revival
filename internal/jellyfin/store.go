package jellyfin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// device: ein angemeldetes Gerät (Zugriffstoken). Gespeichert wird nur der
// SHA-256 des Tokens.
type device struct {
	Hash          string    `json:"hash"`
	Account       string    `json:"account"`
	IsManager     *bool     `json:"is_manager,omitempty"`
	Client        string    `json:"client"`
	ClientVersion string    `json:"client_version"`
	DeviceName    string    `json:"device_name"`
	DeviceID      string    `json:"device_id"`
	Created       time.Time `json:"created"`
	LastSeen      time.Time `json:"last_seen"`
	IP            string    `json:"ip"`
}

// key: Verbindungsschlüssel (vs.JellyfinKeyPrefix + Hash).
func (d *device) key() string { return "jf:" + d.Hash }

// label: Anzeige in „Verbindungen“, z.B. „Infuse · Apple TV“.
func (d *device) label() string {
	c, n := strings.TrimSpace(d.Client), strings.TrimSpace(d.DeviceName)
	switch {
	case c != "" && n != "" && !strings.EqualFold(c, n):
		return c + " · " + n
	case c != "":
		return c
	case n != "":
		return n
	}
	return "Jellyfin-App"
}

// deviceTTL: Geräte, die so lange nicht mehr da waren, werden abgemeldet.
const deviceTTL = 180 * 24 * time.Hour

// persisted: Datei jellyfin.json im Datenordner.
type persisted struct {
	ServerID string             `json:"server_id"`
	Secret   string             `json:"secret"` // für Bild-Tags (HMAC)
	Devices  map[string]*device `json:"devices"`
	// Favorites: Konto → Objekt-Schlüssel ("movie:12", "tvshow:3") → seit
	Favorites map[string]map[string]int64 `json:"favorites,omitempty"`
	// Prefs: Konto → Anzeigeeinstellungen der Apps (DisplayPreferences, roh)
	Prefs map[string]map[string]json.RawMessage `json:"prefs,omitempty"`
}

type store struct {
	mu    sync.Mutex
	path  string
	data  persisted
	dirty bool
	timer *time.Timer
}

func newStore(path string) *store {
	s := &store{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	changed := false
	if len(s.data.ServerID) != 32 {
		s.data.ServerID, changed = randHex(16), true
	}
	if s.data.Secret == "" {
		s.data.Secret, changed = randHex(32), true
	}
	if s.data.Devices == nil {
		s.data.Devices = map[string]*device{}
	}
	if s.data.Favorites == nil {
		s.data.Favorites = map[string]map[string]int64{}
	}
	if s.data.Prefs == nil {
		s.data.Prefs = map[string]map[string]json.RawMessage{}
	}
	for k, d := range s.data.Devices {
		if time.Since(d.LastSeen) > deviceTTL {
			delete(s.data.Devices, k)
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// login legt ein Gerät an und liefert das neue Token. Ein früheres Token
// desselben Geräts (DeviceId) für dieses Konto wird ersetzt – wie bei Jellyfin.
func (s *store) login(d device) (string, *device) {
	tok := randHex(16)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, o := range s.data.Devices {
		if d.DeviceID != "" && o.DeviceID == d.DeviceID && strings.EqualFold(o.Account, d.Account) {
			delete(s.data.Devices, k)
		}
	}
	now := time.Now()
	d.Hash, d.Created, d.LastSeen = hashToken(tok), now, now
	s.data.Devices[d.Hash] = &d
	s.saveLocked()
	cp := d
	return tok, &cp
}

// lookup: Gerät zum Token (Kopie) und aktualisiert „zuletzt gesehen“.
func (s *store) lookup(tok, ip string) (*device, bool) {
	if tok == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data.Devices[hashToken(tok)]
	if !ok {
		return nil, false
	}
	if time.Since(d.LastSeen) > time.Minute || d.IP != ip {
		d.LastSeen, d.IP = time.Now(), ip
		s.saveLaterLocked()
	}
	cp := *d
	return &cp, true
}

func (s *store) alive(hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Devices[hash]
	return ok
}

func (s *store) remove(hash, account string) (*device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data.Devices[hash]
	if !ok || (account != "" && !strings.EqualFold(d.Account, account)) {
		return nil, false
	}
	delete(s.data.Devices, hash)
	s.saveLocked()
	return d, true
}

func (s *store) devices(account string) []device {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []device
	for _, d := range s.data.Devices {
		if account == "" || strings.EqualFold(d.Account, account) {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

func (s *store) serverID() string { return s.data.ServerID }

func (s *store) secret() []byte { return []byte(s.data.Secret) }

func (s *store) favorite(account, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Favorites[strings.ToLower(account)][key]
	return ok
}

func (s *store) setFavorite(account, key string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := strings.ToLower(account)
	m := s.data.Favorites[a]
	if on {
		if m == nil {
			m = map[string]int64{}
			s.data.Favorites[a] = m
		}
		m[key] = time.Now().Unix()
	} else {
		delete(m, key)
	}
	s.saveLocked()
}

func (s *store) prefs(account, id string) (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data.Prefs[strings.ToLower(account)][id]
	return v, ok
}

func (s *store) setPrefs(account, id string, v json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := strings.ToLower(account)
	if s.data.Prefs[a] == nil {
		s.data.Prefs[a] = map[string]json.RawMessage{}
	}
	if len(s.data.Prefs[a]) >= 50 { // Schutz vor beliebig vielen Einträgen
		for k := range s.data.Prefs[a] {
			delete(s.data.Prefs[a], k)
			break
		}
	}
	s.data.Prefs[a][id] = v
	s.saveLocked()
}

// saveLaterLocked bündelt häufige Änderungen (zuletzt gesehen).
func (s *store) saveLaterLocked() {
	if s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(30*time.Second, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.timer = nil
		s.saveLocked()
	})
}

func (s *store) saveLocked() {
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s.data)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *store) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.saveLocked()
}

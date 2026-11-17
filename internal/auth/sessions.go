// Package auth is the Authentication Adapter.
//
// Modus "dsm": SYNO.API.Auth wird unverändert an die DSM-WebAPI weitergereicht.
// DSM prüft Passwort, 2FA, Kontosperre und vergibt die sid; DS Video Revival
// sieht das Passwort nur im Transit und speichert es nie. Für Video-Station-
// Aufrufe wird die sid gegen DSM validiert (Ergebnis kurz zwischengespeichert).
//
// Modus "local": eigene Benutzerdatei mit PBKDF2-SHA256-Hashes (Tests/Entwicklung).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Session maps a sid to an account.
type Session struct {
	Account     string    `json:"account"`
	Created     time.Time `json:"created"`
	LastSeen    time.Time `json:"last_seen"`
	Validated   time.Time `json:"validated"`
	IsManager   *bool     `json:"is_manager,omitempty"`
	SessionName string    `json:"session_name"`
	PrivChecked time.Time `json:"priv_checked"` // letzte Prüfung der Anwendungsberechtigung
	// Revoked: vom Administrator getrennt – die sid bleibt ungültig, auch wenn
	// DSM sie noch kennt (bis zum Ablauf nach SessionTTL).
	Revoked time.Time `json:"revoked,omitempty"`
	// Key: Speicherschlüssel (SHA-256 der sid) – nur zur Laufzeit, kennzeichnet
	// die Verbindung (Benutzerrechte → Verbindungen).
	Key string `json:"-"`
}

// SessionTTL: DSM-Standard laut File Station API Guide ("expires after 7 days").
const SessionTTL = 7 * 24 * time.Hour

// Store keeps sessions; persisted keyed by SHA-256(sid) so that a leaked
// file does not contain usable sids.
type Store struct {
	mu   sync.Mutex
	path string
	m    map[string]*Session // key: hash(sid)
}

// NewStore loads (or creates) the session store.
func NewStore(path string) *Store {
	s := &Store{path: path, m: map[string]*Session{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	s.gc()
	return s
}

// HashSID returns the storage key for a sid.
func HashSID(sid string) string {
	h := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(h[:])
}

// Put registers a session.
func (s *Store) Put(sid string, sess Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.m[HashSID(sid)]; ok && !old.Revoked.IsZero() {
		return // getrennte Sitzung nicht wiederbeleben
	}
	now := time.Now()
	sess.Created, sess.LastSeen = now, now // Validated bleibt leer → erste Nutzung prüft gegen DSM
	sess.Key = ""
	s.m[HashSID(sid)] = &sess
	s.saveLocked()
}

// Get returns a copy of the session (and touches LastSeen).
func (s *Store) Get(sid string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := HashSID(sid)
	sess, ok := s.m[key]
	if !ok || !sess.Revoked.IsZero() {
		return Session{}, false
	}
	if time.Since(sess.LastSeen) > SessionTTL {
		delete(s.m, key)
		s.saveLocked()
		return Session{}, false
	}
	sess.LastSeen = time.Now()
	out := *sess
	out.Key = key
	return out, true
}

// IsRevoked: sid wurde vom Administrator getrennt.
func (s *Store) IsRevoked(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[HashSID(sid)]
	return ok && !sess.Revoked.IsZero()
}

// Revoke trennt die Sitzung mit dem Speicherschlüssel key dauerhaft.
func (s *Store) Revoke(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	sess, ok := s.m[key]
	if !ok {
		sess = &Session{Created: now}
		s.m[key] = sess
	}
	sess.Revoked, sess.LastSeen = now, now
	s.saveLocked()
}

// Alive: Sitzung mit diesem Speicherschlüssel besteht (nicht abgemeldet,
// abgelaufen oder getrennt).
func (s *Store) Alive(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[key]
	return ok && sess.Revoked.IsZero() && time.Since(sess.LastSeen) <= SessionTTL
}

// MarkValidated records a successful upstream validation.
func (s *Store) MarkValidated(sid string, isManager *bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.m[HashSID(sid)]; ok {
		sess.Validated = time.Now()
		if isManager != nil {
			sess.IsManager = isManager
		}
		s.saveLocked()
	}
}

// Delete removes a session.
func (s *Store) Delete(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, HashSID(sid))
	s.saveLocked()
}

// Count returns the number of live sessions.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.m {
		if v.Revoked.IsZero() {
			n++
		}
	}
	return n
}

func (s *Store) gc() {
	for k, v := range s.m {
		if time.Since(v.LastSeen) > SessionTTL {
			delete(s.m, k)
		}
	}
}

func (s *Store) saveLocked() {
	if s.path == "" {
		return
	}
	s.gc()
	b, err := json.Marshal(s.m)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// NewSID creates a random session id (URL-safe, Länge ähnlich DSM-sids).
func NewSID() string {
	b := make([]byte, 48)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

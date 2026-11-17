package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const pbkdf2Iter = 210000

// LocalUser is one entry of users.json (Modus "local").
type LocalUser struct {
	Account   string `json:"account"`
	Hash      string `json:"hash"` // pbkdf2-sha256$iter$salt$hash
	IsManager bool   `json:"is_manager"`
	Disabled  bool   `json:"disabled"`
}

// HashPassword creates a PBKDF2-SHA256 hash string.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(dk)), nil
}

// VerifyPassword checks pw against a hash string (konstante Laufzeit).
func VerifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	var iter int
	if _, err := fmt.Sscanf(parts[1], "%d", &iter); err != nil || iter < 10000 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// LocalUsers is the users.json backend.
type LocalUsers struct {
	path  string
	mu    sync.Mutex
	users map[string]LocalUser
	mtime time.Time
}

// NewLocalUsers loads users.json.
func NewLocalUsers(path string) (*LocalUsers, error) {
	l := &LocalUsers{path: path}
	return l, l.reload()
}

func (l *LocalUsers) reload() error {
	st, err := os.Stat(l.path)
	if err != nil {
		return err
	}
	if st.ModTime().Equal(l.mtime) && l.users != nil {
		return nil
	}
	b, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	var list []LocalUser
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	m := map[string]LocalUser{}
	for _, u := range list {
		m[strings.ToLower(u.Account)] = u
	}
	l.users, l.mtime = m, st.ModTime()
	return nil
}

// ErrDisabled is returned for disabled accounts.
var ErrDisabled = errors.New("account disabled")

// Check verifies credentials.
func (l *LocalUsers) Check(account, pw string) (LocalUser, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.reload()
	u, ok := l.users[strings.ToLower(account)]
	if !ok {
		// Gleiche Rechenzeit wie bei existierendem Konto (Timing-Schutz).
		VerifyPassword("pbkdf2-sha256$210000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", pw)
		return LocalUser{}, errors.New("bad credentials")
	}
	if !VerifyPassword(u.Hash, pw) {
		return LocalUser{}, errors.New("bad credentials")
	}
	if u.Disabled {
		return LocalUser{}, ErrDisabled
	}
	return u, nil
}

// RateLimiter blocks a client IP after too many failed logins.
// Wichtig im DSM-Modus: DSM sieht alle Anmeldungen von 127.0.0.1; ohne eigene
// Begrenzung könnte die DSM-Autoblockierung den Proxy selbst sperren.
type RateLimiter struct {
	mu     sync.Mutex
	fails  map[string][]time.Time
	Max    int
	Window time.Duration
}

// NewRateLimiter: max Fehlversuche je Fenster.
func NewRateLimiter(max int, window time.Duration) *RateLimiter {
	return &RateLimiter{fails: map[string][]time.Time{}, Max: max, Window: window}
}

// Blocked reports whether ip is currently blocked.
func (r *RateLimiter) Blocked(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prune(ip)) >= r.Max
}

// Fail records a failed attempt.
func (r *RateLimiter) Fail(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fails[ip] = append(r.prune(ip), time.Now())
}

// Success clears the counter.
func (r *RateLimiter) Success(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.fails, ip)
}

func (r *RateLimiter) prune(ip string) []time.Time {
	cut := time.Now().Add(-r.Window)
	keep := r.fails[ip][:0]
	for _, t := range r.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(r.fails, ip)
		return nil
	}
	r.fails[ip] = keep
	return keep
}

// Names lists the local accounts (Auswahl in der Weboberfläche).
func (l *LocalUsers) Names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.reload()
	out := make([]string, 0, len(l.users))
	for _, u := range l.users {
		out = append(out, u.Account)
	}
	return out
}

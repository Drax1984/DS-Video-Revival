package vs

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// Recorder protokolliert alle Aufrufe, die (noch) nicht implementiert sind.
// Das ist die Feld-Rückkopplung: Ein Test mit einem Original-Client zeigt
// sofort, welche APIs/Methoden/additional-Felder der Client tatsächlich nutzt.
// Gespeichert werden nur Parameter-NAMEN (keine Werte, keine Zugangsdaten).
type Recorder struct {
	mu     sync.Mutex
	path   string
	counts map[string]*Unhandled
}

// Unhandled is an aggregated entry.
type Unhandled struct {
	Key       string    `json:"key"`
	API       string    `json:"api"`
	Version   int       `json:"version"`
	Method    string    `json:"method"`
	Note      string    `json:"note"`
	Params    []string  `json:"params"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// NewRecorder loads previous entries.
func NewRecorder(path string) *Recorder {
	r := &Recorder{path: path, counts: map[string]*Unhandled{}}
	if b, err := os.ReadFile(path); err == nil {
		var list []*Unhandled
		if json.Unmarshal(b, &list) == nil {
			for _, u := range list {
				r.counts[u.Key] = u
			}
		}
	}
	return r
}

// Record adds an occurrence.
func (r *Recorder) Record(api string, version int, method, note string, params []string) {
	sort.Strings(params)
	key := api + "|" + method + "|" + note
	r.mu.Lock()
	defer r.mu.Unlock()
	u := r.counts[key]
	now := time.Now()
	if u == nil {
		u = &Unhandled{Key: key, API: api, Version: version, Method: method, Note: note, FirstSeen: now}
		r.counts[key] = u
	}
	u.Count++
	u.LastSeen = now
	u.Version = version
	u.Params = mergeKeys(u.Params, params)
	r.saveLocked()
}

func mergeKeys(a, b []string) []string {
	set := map[string]bool{}
	for _, k := range append(append([]string{}, a...), b...) {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// List returns entries sorted by count.
func (r *Recorder) List() []Unhandled {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Unhandled, 0, len(r.counts))
	for _, u := range r.counts {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// Reset clears all entries.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts = map[string]*Unhandled{}
	r.saveLocked()
}

func (r *Recorder) saveLocked() {
	if r.path == "" {
		return
	}
	list := make([]*Unhandled, 0, len(r.counts))
	for _, u := range r.counts {
		list = append(list, u)
	}
	b, _ := json.MarshalIndent(list, "", " ")
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, r.path)
	}
}

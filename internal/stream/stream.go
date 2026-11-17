// Package stream is the Streaming Engine (Komponente F).
//
// PoC-Stand (Phase 3, Direct Play): Original-Datei wird byteweise mit
// Range-Unterstützung ausgeliefert ("raw"). Belegt durch holg/ds-video-hevc:
// DS Video tvOS akzeptiert für ein mit hls_remux geöffnetes Stream-Objekt die
// rohe Datei (HTTP 206, MP4), wenn open {"format":"hls_remux","stream_id":…}
// zurückgibt. Echtes HLS-Remux/Transcoding (FFmpeg) folgt in Phase 3/5.
package stream

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Session is an open stream.
type Session struct {
	ID       string
	Account  string
	FileID   int
	Path     string
	Format   string // gemeldetes Format (hls_remux | raw)
	Device   string
	HLSJob   string // gesetzt, wenn per FFmpeg nach HLS umgewandelt wird
	Opened   time.Time
	LastUsed time.Time
}

// Manager keeps stream sessions.
type Manager struct {
	mu sync.Mutex
	m  map[string]*Session
	// IdleTimeout schließt verwaiste Streams.
	IdleTimeout time.Duration
}

// NewManager creates a manager and starts the janitor.
func NewManager() *Manager {
	mg := &Manager{m: map[string]*Session{}, IdleTimeout: 6 * time.Hour}
	go func() {
		for range time.Tick(10 * time.Minute) {
			mg.gc()
		}
	}()
	return mg
}

// NewStreamID mimics the observed id format "<32 hex>_<8 alnum>"
// (Beispielantwort SYNO.VideoStation2.Streaming open, Community-Doku).
func NewStreamID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	suf := make([]byte, 8)
	for i := range suf {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alnum))))
		suf[i] = alnum[n.Int64()]
	}
	return hex.EncodeToString(b) + "_" + string(suf)
}

// Open registers a stream.
func (mg *Manager) Open(account string, fileID int, path, format, device string) *Session {
	s := &Session{ID: NewStreamID(), Account: account, FileID: fileID, Path: path, Format: format, Device: device, Opened: time.Now(), LastUsed: time.Now()}
	mg.mu.Lock()
	mg.m[s.ID] = s
	mg.mu.Unlock()
	return s
}

// Get returns a stream if it belongs to account.
func (mg *Manager) Get(id, account string) (*Session, error) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	s, ok := mg.m[id]
	if !ok {
		return nil, errors.New("unknown stream")
	}
	if s.Account != account {
		return nil, errors.New("stream belongs to another user")
	}
	s.LastUsed = time.Now()
	return s, nil
}

// Attach links an HLS job to a stream.
func (mg *Manager) Attach(id, job string) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	if s, ok := mg.m[id]; ok {
		s.HLSJob = job
	}
}

// Close removes a stream.
func (mg *Manager) Close(id, account string) bool {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	s, ok := mg.m[id]
	if !ok || s.Account != account {
		return false
	}
	delete(mg.m, id)
	return true
}

// Count returns open streams.
func (mg *Manager) Count() int {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return len(mg.m)
}

func (mg *Manager) gc() {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	for id, s := range mg.m {
		if time.Since(s.LastUsed) > mg.IdleTimeout {
			delete(mg.m, id)
		}
	}
}

// ServeRaw delivers the file with Range support (Direct Play).
func ServeRaw(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file not readable", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.Error(w, "not a file", http.StatusNotFound)
		return
	}
	ct := ContentType(path)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")
	// ServeContent übernimmt Range/If-Range/HEAD korrekt (206/416).
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// ContentType maps video extensions to MIME types.
func ContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv":
		return "video/x-matroska"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".avi", ".divx":
		return "video/x-msvideo"
	case ".ts", ".m2ts", ".mts":
		return "video/mp2t"
	case ".webm":
		return "video/webm"
	case ".mpg", ".mpeg", ".vob":
		return "video/mpeg"
	case ".wmv":
		return "video/x-ms-wmv"
	}
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// AppleNative reports containers AVPlayer (tvOS/iOS) plays without remux.
func AppleNative(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov":
		return true
	}
	return false
}

package jellyfin

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Zugriffsprotokoll für Wiedergabe-Adressen (/videos/…): zeigt, was der
// Player der App wirklich abruft (Apple-Player, Web-Fenster, Teilabfragen)
// und was er bekommt – im Protokoll und in der Fehlermeldung der Mediathek.

type accessEntry struct {
	At     time.Time `json:"At"`
	Method string    `json:"Method"`
	Path   string    `json:"Path"`
	Range  string    `json:"Range,omitempty"`
	Status int       `json:"Status"`
	Bytes  int64     `json:"Bytes"`
	Agent  string    `json:"Agent"`
	Ms     int64     `json:"Ms"`
}

type accessLog struct {
	mu   sync.Mutex
	ring []accessEntry
	segs map[string]int // protokollierte Segmente je Umwandlung
}

// logSegment: die ersten Segmente jeder Umwandlung ins Protokoll (danach nur
// Fehler und langsame Antworten).
func (a *accessLog) logSegment(job string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.segs == nil || len(a.segs) > 200 {
		a.segs = map[string]int{}
	}
	a.segs[job]++
	return a.segs[job] <= 4
}

const accessKeep = 80

func (a *accessLog) add(e accessEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ring = append(a.ring, e)
	if len(a.ring) > accessKeep {
		a.ring = append([]accessEntry(nil), a.ring[len(a.ring)-accessKeep:]...)
	}
}

func (a *accessLog) since(t time.Time) []accessEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []accessEntry{}
	for _, e := range a.ring {
		if !e.At.Before(t) {
			out = append(out, e)
		}
	}
	return out
}

type statusWriter struct {
	http.ResponseWriter
	code  int
	bytes int64
}

func (s *statusWriter) WriteHeader(c int) {
	if s.code == 0 {
		s.code = c
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// shortPath: Pfad ohne Abfrage, lange IDs gekürzt.
func shortPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		if len(s) > 16 {
			parts[i] = s[:6] + "…"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func shortAgent(ua string) string {
	switch {
	case strings.Contains(ua, "AppleCoreMedia"):
		return "AppleCoreMedia"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		return "WebView iOS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case ua == "":
		return "-"
	}
	if len(ua) > 24 {
		return ua[:24]
	}
	return ua
}

// trackAccess umschließt w für Aufrufe unter /videos/; done() trägt ein.
func (h *Handler) trackAccess(w http.ResponseWriter, r *http.Request, segs []string) (http.ResponseWriter, func()) {
	if len(segs) < 2 || segs[0] != "videos" {
		return w, func() {}
	}
	sw := &statusWriter{ResponseWriter: w}
	start := time.Now()
	return sw, func() {
		e := accessEntry{At: start, Method: r.Method, Path: shortPath(r.URL.Path), Range: r.Header.Get("Range"),
			Status: sw.code, Bytes: sw.bytes, Agent: shortAgent(r.UserAgent()), Ms: time.Since(start).Milliseconds()}
		if e.Status == 0 {
			e.Status = http.StatusOK
		}
		h.access.add(e)
		seg := strings.HasSuffix(e.Path, ".m4s") || strings.HasSuffix(e.Path, ".ts")
		job := ""
		if len(segs) >= 5 && segs[2] == "hls" {
			job = segs[3]
		}
		if !seg || e.Status >= 300 && e.Status != http.StatusPartialContent || e.Ms >= 3000 || h.access.logSegment(job) {
			rg := ""
			if e.Range != "" {
				rg = " [" + e.Range + "]"
			}
			log.Printf("jellyfin-zugriff: %s %s%s → %d, %d B (%s, %d ms)", e.Method, e.Path, rg, e.Status, e.Bytes, e.Agent, e.Ms)
		}
	}
}

// diagRequests: GET /DsvrDiag/Requests – Wiedergabe-Abrufe der letzten Minuten.
func (h *Handler) diagRequests(x *req) {
	writeJSON(x.w, h.access.since(time.Now().Add(-3*time.Minute)))
}

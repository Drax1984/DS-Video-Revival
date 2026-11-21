package jellyfin

import (
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// ---------- Übertragung beim Herunterladen ----------
//
// Die App „Jellyfin“ lädt Dateien selbst herunter und meldet der Seite den
// Fortschritt nicht. Der Server sieht die Übertragung aber: Je Anmeldung
// (Token) und Titel zählt er die gesendeten Bytes, damit die Mediathek
// anzeigen kann, wann die Datei wirklich auf dem Handy ist. Gezählt werden
// nur vollständige Abrufe (ohne Range bzw. „bytes=N-“ beim Fortsetzen) –
// Player streamen mit Teilbereichen und zählen daher nicht.

type transfer struct {
	Total   int64
	Sent    int64
	Done    bool
	Started time.Time
	Updated time.Time
}

type transfers struct {
	mu sync.Mutex
	m  map[string]*transfer
}

func (t *transfers) get(token, item string) (transfer, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr, ok := t.m[token+"|"+normID(item)]; ok {
		return *tr, true
	}
	return transfer{}, false
}

func (t *transfers) gc() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, tr := range t.m {
		if time.Since(tr.Updated) > 24*time.Hour {
			delete(t.m, k)
		}
	}
}

var reOpenRange = regexp.MustCompile(`^bytes=(\d+)-$`)

type countWriter struct {
	http.ResponseWriter
	t  *transfers
	tr *transfer
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.t.mu.Lock()
	c.tr.Sent += int64(n)
	c.tr.Updated = time.Now()
	c.t.mu.Unlock()
	return n, err
}

func (c *countWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// track liefert einen Writer, der die Übertragung von item (Größe total)
// mitzählt, und eine Funktion für das Ende des Abrufs.
func (h *Handler) track(x *req, item string, total int64) (http.ResponseWriter, func()) {
	if x.r.Method != http.MethodGet || total <= 0 || x.auth.Token == "" {
		return x.w, func() {}
	}
	off := int64(0)
	if rg := x.r.Header.Get("Range"); rg != "" {
		m := reOpenRange.FindStringSubmatch(rg)
		if m == nil {
			return x.w, func() {} // Teilbereich: Player, kein Herunterladen
		}
		off, _ = strconv.ParseInt(m[1], 10, 64)
	}
	key := x.auth.Token + "|" + normID(item)
	h.tr.mu.Lock()
	if h.tr.m == nil {
		h.tr.m = map[string]*transfer{}
	}
	tr := &transfer{Total: total, Sent: off, Started: time.Now(), Updated: time.Now()}
	h.tr.m[key] = tr
	h.tr.mu.Unlock()
	return &countWriter{ResponseWriter: x.w, t: &h.tr, tr: tr}, func() {
		h.tr.mu.Lock()
		tr.Done = tr.Sent >= tr.Total
		tr.Updated = time.Now()
		h.tr.mu.Unlock()
	}
}

// transferState für /DsvrOffline/{id}: Stand der letzten Übertragung des Titels
// an diese Anmeldung (nil = keine).
func (h *Handler) transferState(x *req, item string) map[string]any {
	tr, ok := h.tr.get(x.auth.Token, item)
	if !ok {
		return nil
	}
	rate := 0.0
	if el := tr.Updated.Sub(tr.Started).Seconds(); el > 0.5 {
		rate = float64(tr.Sent) / el
	}
	left := -1
	if rate > 0 && !tr.Done {
		left = int(float64(tr.Total-tr.Sent) / rate)
	}
	// Bricht die App ab (oder pausiert sie im Hintergrund), kommt nichts mehr an.
	stalled := !tr.Done && time.Since(tr.Updated) > 20*time.Second
	return map[string]any{"Total": tr.Total, "Sent": tr.Sent, "Done": tr.Done, "TimeLeft": left, "Stalled": stalled}
}

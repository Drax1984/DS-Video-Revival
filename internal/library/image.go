package library

import (
	"bytes"
	"io"
	"os"
	"sync"
	"time"
)

// ValidImage: Bilddatei vorhanden, lesbar und als JPEG, PNG, WebP oder GIF
// erkennbar. Ergebnis je Pfad zwischengespeichert (neu geprüft, sobald sich
// Größe oder Änderungszeit ändern) – fehlende, leere oder kaputte Plakate
// (z.B. nach Verschieben des Filmordners) fallen so auf das Plakat aus den
// Metadaten zurück, statt als defektes Bild zu erscheinen.
func ValidImage(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() < 8 {
		return false
	}
	imgCache.Lock()
	c, ok := imgCache.m[p]
	imgCache.Unlock()
	if ok && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		return c.ok
	}
	res := sniffImage(p)
	imgCache.Lock()
	if len(imgCache.m) > 20000 {
		imgCache.m = map[string]imgCheck{}
	}
	imgCache.m[p] = imgCheck{size: st.Size(), mtime: st.ModTime(), ok: res}
	imgCache.Unlock()
	return res
}

type imgCheck struct {
	size  int64
	mtime time.Time
	ok    bool
}

var imgCache = struct {
	sync.Mutex
	m map[string]imgCheck
}{m: map[string]imgCheck{}}

func sniffImage(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 12)
	if _, err := io.ReadFull(f, b); err != nil {
		return false
	}
	return IsImage(b)
}

// IsImage: Dateianfang eines JPEG-, PNG-, WebP- oder GIF-Bildes.
func IsImage(b []byte) bool {
	switch {
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return true
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return true
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return true
	}
	return false
}

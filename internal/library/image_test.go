package library

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPosterFallback: fehlendes, leeres oder kaputtes lokales Plakat → Plakat
// aus den Metadaten.
func TestPosterFallback(t *testing.T) {
	dir := t.TempDir()
	jpg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01")
	meta := filepath.Join(dir, "movie-1-poster.jpg")
	os.WriteFile(meta, jpg, 0o644)
	empty := filepath.Join(dir, "leer.jpg")
	os.WriteFile(empty, nil, 0o644)
	html := filepath.Join(dir, "kaputt.jpg")
	os.WriteFile(html, []byte("<html>Fehler</html>"), 0o644)
	local := filepath.Join(dir, "poster.jpg")
	os.WriteFile(local, jpg, 0o644)
	for local0, want := range map[string]string{
		filepath.Join(dir, "weg", "poster.jpg"): meta,
		empty:                                   meta,
		html:                                    meta,
		local:                                   local,
	} {
		it := Item{Poster: local0, Meta: &Meta{Poster: meta}}
		if got := it.PosterPath(); got != want {
			t.Errorf("%s → %s, erwartet %s", local0, got, want)
		}
	}
	if (Item{Meta: &Meta{Poster: html}}).PosterPath() != "" {
		t.Error("kaputtes Metadaten-Plakat geliefert")
	}
	// Datei wird repariert → neu geprüft
	os.WriteFile(html, append(jpg, 'x'), 0o644)
	if !ValidImage(html) {
		t.Error("geänderte Datei nicht neu geprüft")
	}
}

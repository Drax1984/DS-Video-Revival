package library

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"dsvideorevival/internal/config"
)

// Titel/Ordner mit „#“ oder „!“ am Anfang werden eingelesen, Synology-
// Systemordner (#recycle, #snapshot, @eaDir) weiterhin nicht.
func TestScanSpecialNames(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"#1 Kino (2019)/#1 Kino (2019).mkv",
		"!Neu/Film C (2001).mkv",
		"#Film (2020).mkv",
		"!Film (2021).mp4",
		"#recycle/Alt (1999).mkv",
		"#snapshot/Snap (1998).mkv",
		"@eaDir/X (1997).mkv",
	} {
		f := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		_ = os.WriteFile(f, []byte("x"), 0o644)
	}
	ix, err := Open(filepath.Join(t.TempDir(), "lib.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Scan([]config.Folder{{Path: root, Type: "movie"}}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range ix.data.Items {
		got = append(got, it.DisplayTitle())
	}
	sort.Strings(got)
	want := []string{"!Film", "#1 Kino", "#Film", "Film C"}
	if len(got) != len(want) {
		t.Fatalf("Titel: %q, erwartet %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Titel: %q, erwartet %q", got, want)
		}
	}
}

// Von Hand gesetzter Titel hat Vorrang; leer = wieder automatisch.
func TestCustomTitle(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "Film B (2001).mkv"), []byte("x"), 0o644)
	ix, _ := Open(filepath.Join(t.TempDir(), "lib.json"))
	if _, err := ix.Scan([]config.Folder{{Path: root, Type: "movie"}}); err != nil {
		t.Fatal(err)
	}
	var id int
	for i := range ix.data.Items {
		id = i
	}
	if !ix.SetTitle("movie", id, "Mein Titel") {
		t.Fatal("SetTitle")
	}
	it, _ := ix.Item(id)
	if it.DisplayTitle() != "Mein Titel" || it.SortTitle != "Mein Titel" {
		t.Fatalf("%+v", it)
	}
	// Neu-Scan behält den Titel
	if _, err := ix.Scan([]config.Folder{{Path: root, Type: "movie"}}); err != nil {
		t.Fatal(err)
	}
	if it, _ = ix.Item(id); it.DisplayTitle() != "Mein Titel" {
		t.Fatalf("nach Scan: %q", it.DisplayTitle())
	}
	ix.SetTitle("movie", id, "")
	if it, _ = ix.Item(id); it.DisplayTitle() != "Film B" {
		t.Fatalf("zurückgesetzt: %q", it.DisplayTitle())
	}
	ix.RemoveFiles(it.FileIDs)
	if _, ok := ix.Item(id); ok {
		t.Fatal("Objekt nach RemoveFiles noch vorhanden")
	}
}

func TestParseRecording(t *testing.T) {
	p := ParseName("Aufnahmen/20231105 2015 - Das Erste HD - Tatort.ts", "tv_record")
	if p.Kind != "tv_record" || p.Title != "Tatort" || p.Channel != "Das Erste HD" || p.Recorded != "2023-11-05 20:15:00" {
		t.Fatalf("%+v", p)
	}
	if p := ParseName("Mitschnitt Konzert.ts", "tv_record"); p.Title != "Mitschnitt Konzert" || p.Channel != "" {
		t.Fatalf("%+v", p)
	}
}

// Entfernte Medienordner verschwinden beim nächsten Scan, Bibliothekswechsel ordnet neu ein.
func TestScanRemovedFolderAndLibrary(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(a, "Film A (2001).mkv"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(b, "Urlaub 2019.mp4"), []byte("x"), 0o644)
	ix, _ := Open(filepath.Join(t.TempDir(), "lib.json"))
	fa, fb := config.Folder{Path: a, Type: "movie"}, config.Folder{Path: b, Type: "home_video"}
	if _, err := ix.Scan([]config.Folder{fa, fb}); err != nil {
		t.Fatal(err)
	}
	if len(ix.data.Items) != 2 {
		t.Fatalf("%d Objekte", len(ix.data.Items))
	}
	if _, err := ix.Scan([]config.Folder{fa}); err != nil {
		t.Fatal(err)
	}
	for _, it := range ix.data.Items {
		if it.Kind == "home_video" {
			t.Fatal("Heimvideo aus entferntem Ordner noch da")
		}
	}
	fa.LibraryID = 7
	if _, err := ix.Scan([]config.Folder{fa}); err != nil {
		t.Fatal(err)
	}
	for _, it := range ix.data.Items {
		if it.LibraryID != 7 {
			t.Fatalf("Bibliothek nicht übernommen: %+v", it)
		}
	}
}

package library

import (
	"os"
	"path/filepath"
	"testing"

	"dsvideorevival/internal/config"
)

// Typ eines Medienordners nachträglich ändern: vorhandene Dateien werden neu eingeordnet.
func TestScanFolderTypeChange(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "Dark", "Season 01", "Dark - S01E02.mkv")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("x"), 0o644)
	ix, err := Open(filepath.Join(t.TempDir(), "lib.json"))
	if err != nil {
		t.Fatal(err)
	}
	kinds := func() map[string]int {
		m := map[string]int{}
		for _, it := range ix.data.Items {
			m[it.Kind]++
		}
		return m
	}
	if _, err := ix.Scan([]config.Folder{{Path: root, Type: "home_video"}}); err != nil {
		t.Fatal(err)
	}
	if k := kinds(); k["home_video"] != 1 || len(k) != 1 {
		t.Fatalf("vorher: %v", k)
	}
	if _, err := ix.Scan([]config.Folder{{Path: root, Type: "tvshow"}}); err != nil {
		t.Fatal(err)
	}
	if k := kinds(); k["episode"] != 1 || len(k) != 1 || len(ix.data.Files) != 1 {
		t.Fatalf("nachher: %v, %d Dateien", k, len(ix.data.Files))
	}
}

package library

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"dsvideorevival/internal/config"
)

// TestScanLoad: Belastungstest (nur mit DSVR_LOAD=1), misst Scan-Dauer und Heap.
func TestScanLoad(t *testing.T) {
	if os.Getenv("DSVR_LOAD") == "" {
		t.Skip("DSVR_LOAD=1 setzen")
	}
	for _, n := range []int{10000, 50000} {
		root := filepath.Join(t.TempDir(), "m")
		for i := 0; i < n; i++ {
			var p string
			if i%2 == 0 {
				p = filepath.Join(root, "Filme", fmt.Sprintf("Film Nummer %d (%d)", i, 1950+i%70), fmt.Sprintf("Film Nummer %d (%d).mkv", i, 1950+i%70))
			} else {
				p = filepath.Join(root, "Serien", fmt.Sprintf("Serie %d", i/200), fmt.Sprintf("Season %02d", (i/20)%10+1), fmt.Sprintf("Serie %d - S%02dE%02d.mkv", i/200, (i/20)%10+1, i%20+1))
			}
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, nil, 0o644)
		}
		ix, _ := Open(filepath.Join(t.TempDir(), "lib.json"))
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		res, err := ix.Scan([]config.Folder{{Path: filepath.Join(root, "Filme"), Type: "movie"}, {Path: filepath.Join(root, "Serien"), Type: "tvshow"}})
		d := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		start2 := time.Now()
		res2, _ := ix.Scan([]config.Folder{{Path: filepath.Join(root, "Filme"), Type: "movie"}, {Path: filepath.Join(root, "Serien"), Type: "tvshow"}})
		d2 := time.Since(start2)
		runtime.GC()
		runtime.ReadMemStats(&after)
		qs := time.Now()
		_, total := ix.Items(Query{Kind: "movie", Limit: 50, SortBy: "title"})
		t.Logf("%d Dateien: Erstscan %s (+%d), Folgescan %s (+%d), Heap +%.1f MB, Filmliste (%d) in %s",
			n, d.Round(time.Millisecond), res.Added, d2.Round(time.Millisecond), res2.Added, float64(after.HeapAlloc-before.HeapAlloc)/1e6, total, time.Since(qs).Round(time.Microsecond))
	}
}

package library

import (
	"os"
	"path/filepath"
	"testing"

	"dsvideorevival/internal/config"
)

func TestAccessFilter(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	mk("Filme/Kinderfilm (2020)/Kinderfilm (2020).mkv")
	mk("Filme/Horror (2021)/Horror (2021).mkv")
	mk("Filme/Ohne (2022)/Ohne (2022).mkv")
	mk("Heim/Urlaub 2023.mp4")
	mk("Serien/Dark/Season 01/Dark - S01E01.mkv")
	ix, err := Open(filepath.Join(t.TempDir(), "lib.json"))
	if err != nil {
		t.Fatal(err)
	}
	folders := []config.Folder{{Path: filepath.Join(root, "Filme"), Type: "movie"}, {Path: filepath.Join(root, "Heim"), Type: "home_video"}, {Path: filepath.Join(root, "Serien"), Type: "tvshow"}}
	if _, err := ix.Scan(folders); err != nil {
		t.Fatal(err)
	}
	cert := map[string]string{"Kinderfilm": "FSK 6", "Horror": "FSK 18"}
	for _, it := range ix.data.Items {
		if c, ok := cert[it.Title]; ok {
			it.Meta = &Meta{Certificate: c}
		}
	}
	for _, sh := range ix.data.Shows {
		sh.Meta = &Meta{Certificate: "FSK 16"}
	}
	titles := func(a *Access) map[string]bool {
		out := map[string]bool{}
		v := ix.For(a)
		for _, k := range []string{"movie", "home_video", "episode"} {
			its, _ := v.Items(Query{Kind: k})
			for _, it := range its {
				out[it.Title] = true
			}
		}
		shows, _ := v.Shows(nil, 0, 0)
		for _, s := range shows {
			out["Serie:"+s.Title] = true
		}
		return out
	}
	all := titles(nil)
	if len(all) != 6 {
		t.Fatalf("alle: %v", all)
	}
	kid := titles(&Access{Folders: []string{filepath.Join(root, "Filme"), filepath.Join(root, "Serien")}, MaxFSK: 12, BlockUnrated: true})
	if !kid["Kinderfilm"] || kid["Horror"] || kid["Ohne"] || kid["Urlaub 2023"] || kid["Serie:Dark"] || len(kid) != 1 {
		t.Fatalf("Kind: %v", kid)
	}
	teen := titles(&Access{Folders: nil, MaxFSK: 16})
	if !teen["Ohne"] || !teen["Serie:Dark"] || teen["Horror"] {
		t.Fatalf("FSK 16 ohne Sperre unbewerteter: %v", teen)
	}
	// Einzelzugriffe gefiltert
	v := ix.For(&Access{MaxFSK: 12, BlockUnrated: true})
	for _, it := range ix.data.Items {
		_, ok := v.Item(it.ID)
		_, fok := v.File(it.FileIDs[0])
		if want := it.Title == "Kinderfilm"; ok != want || fok != want {
			t.Fatalf("%s: Item %v File %v", it.Title, ok, fok)
		}
	}
	// FSK von Hand
	for _, it := range ix.data.Items {
		if it.Title == "Horror" {
			n := 12
			ix.SetFSK("movie", it.ID, &n)
			if _, ok := v.Item(it.ID); !ok {
				t.Fatal("manuelle FSK 12 nicht wirksam")
			}
		}
	}
	if ParseFSK("FSK 16") != 16 || ParseFSK("PG-13") != -1 || ParseFSK("12") != 12 {
		t.Fatal("ParseFSK")
	}
}

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/vs"
)

func TestMoveToRecycle(t *testing.T) {
	root := t.TempDir()
	rc := filepath.Join(root, "#recycle")
	_ = os.MkdirAll(rc, 0o755)
	for i := 0; i < 2; i++ {
		p := filepath.Join(root, "Filme", "Film (2020).mkv")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		if err := moveToRecycle(p, rc, root); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"Film (2020).mkv", "Film (2020) (1).mkv"} {
		if _, err := os.Stat(filepath.Join(rc, "Filme", n)); err != nil {
			t.Fatalf("%s fehlt im Papierkorb: %v", n, err)
		}
	}
}

func TestDeleteAndTitle(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "Film B (2001).mkv")
	_ = os.WriteFile(p, []byte("x"), 0o644)
	lib, _ := library.Open(filepath.Join(t.TempDir(), "lib.json"))
	if _, err := lib.Scan([]config.Folder{{Path: root, Type: "movie"}}); err != nil {
		t.Fatal(err)
	}
	items, _ := lib.Items(library.Query{Kind: "movie"})
	id := items[0].ID
	h := &Handler{S: &vs.Server{Lib: lib}}
	sess := auth.Session{Account: "admin"}
	post := func(fn func(*Handler, *httptest.ResponseRecorder, string), body string) map[string]any {
		rec := httptest.NewRecorder()
		fn(h, rec, body)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	title := func(h *Handler, w *httptest.ResponseRecorder, b string) {
		h.setTitle(w, httptest.NewRequest("POST", "/dsvr/api/title", strings.NewReader(b)), sess)
	}
	del := func(h *Handler, w *httptest.ResponseRecorder, b string) {
		h.deleteItem(w, httptest.NewRequest("POST", "/dsvr/api/delete", strings.NewReader(b)), sess, "")
	}
	if m := post(title, `{"kind":"movie","id":`+itoa(id)+`,"title":"  Neuer   Titel "}`); m["title"] != "Neuer Titel" {
		t.Fatalf("Titel: %v", m)
	}
	if m := post(del, `{"kind":"movie","id":`+itoa(id)+`}`); m["ok"] != true {
		t.Fatalf("Löschen: %v", m)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("Datei noch vorhanden")
	}
	if _, ok := lib.Item(id); ok {
		t.Fatal("Objekt noch im Index")
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// TestDSMDelete: Löschen über die File Station API von DSM (Rückfall, wenn
// der Paketbenutzer im Ordner nicht schreiben darf).
func TestDSMDelete(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form
		if r.Form.Get("_sid") != "SID1" {
			_, _ = w.Write([]byte(`{"success":false,"error":{"code":119}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	up, err := upstream.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{S: &vs.Server{Up: up}}
	if err := h.dsmDelete(context.Background(), "SID1", "/volume1/Downloads/Film/Sample/film.sample.mkv"); err != nil {
		t.Fatal(err)
	}
	if got.Get("api") != "SYNO.FileStation.Delete" || got.Get("method") != "delete" || got.Get("version") != "2" ||
		got.Get("path") != `"/Downloads/Film/Sample/film.sample.mkv"` || got.Get("recursive") != "false" {
		t.Fatalf("Anfrage: %v", got)
	}
	if err := h.dsmDelete(context.Background(), "falsch", "/volume1/Downloads/x.mkv"); err == nil {
		t.Fatal("Fehler von DSM nicht gemeldet")
	}
	if err := h.dsmDelete(context.Background(), "SID1", "/tmp/x.mkv"); err == nil {
		t.Fatal("Pfad außerhalb einer Freigabe angenommen")
	}
}

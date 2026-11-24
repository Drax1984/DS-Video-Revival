package jellyfin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestAndroidDownload: Herunterladen wie die App „Jellyfin“ für Android
// (2.7): Sie holt den Titel über /Items?ids=<UUID>&fields=MediaSources,Path
// (ohne „Path“ bricht sie mit „Missing item path“ ab und hinterlässt leere
// Dateien), lädt dann Plakat und Datei mit „Range: bytes=0-“ und
// Authorization-Kopfzeile. Die Übertragung wird für die Mediathek gezählt.
func TestAndroidDownload(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna")
	dashed := func(id string) string {
		return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	}
	get := func(u string) (*http.Response, int64) {
		req, _ := http.NewRequest("GET", e.ts.URL+u, nil)
		req.Header.Set("Authorization", `MediaBrowser Client="Jellyfin Android", Version="2.7.3", DeviceId="and-1", Device="SM-G991B", Token="`+tok+`"`)
		req.Header.Set("Range", "bytes=0-")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp, n
	}
	var film map[string]any
	for _, it := range items(e.json("GET", "/Items?Recursive=true&IncludeItemTypes=Movie", tok, nil)) {
		if it["Name"] == "Film A" {
			film = it
		}
	}
	id := film["Id"].(string)
	q := url.Values{"ids": {dashed(id)}, "fields": {"MediaSources,Path"}}
	got := items(e.json("GET", "/Items?"+q.Encode(), tok, nil))
	if len(got) != 1 {
		t.Fatalf("Titel: %v", got)
	}
	p, _ := got[0]["Path"].(string)
	if p == "" || strings.ContainsAny(p, `/\`) {
		t.Fatalf("Path: nur Dateiname erwartet, nicht %q", p)
	}
	if ms, _ := got[0]["MediaSources"].([]any); len(ms) == 0 {
		t.Fatal("MediaSources fehlen (Offline-Wiedergabe der App)")
	}
	tag := got[0]["ImageTags"].(map[string]any)["Primary"].(string)
	for _, u := range []string{"/Items/" + dashed(id) + "/Images/Primary?tag=" + tag + "&format=Webp", "/Items/" + dashed(id) + "/Download"} {
		resp, n := get(u)
		if resp.StatusCode != 206 || resp.Header.Get("Content-Range") == "" || n == 0 {
			t.Fatalf("%s: %d %q (%d Bytes)", u, resp.StatusCode, resp.Header.Get("Content-Range"), n)
		}
	}
	if tr, ok := e.h.tr.get(tok, id); !ok || !tr.Done || tr.Sent != tr.Total {
		t.Fatalf("Übertragung nicht gezählt: %+v", tr)
	}
	// Ohne „Path“ in den Feldern bleibt es weg (wie bei Jellyfin).
	if _, ok := items(e.json("GET", "/Items?ids="+id, tok, nil))[0]["Path"]; ok {
		t.Fatal("Path ohne Anforderung geliefert")
	}
}

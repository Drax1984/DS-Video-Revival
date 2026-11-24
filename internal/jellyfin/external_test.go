package jellyfin

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
)

// TestExternalPlayer: App „Jellyfin“ für Android mit externem Player (VLC):
// Die App fragt die Wiedergabeinfo mit ihrem Profil für externe Player ab
// (alles direkt) und gibt VLC die Adresse /Videos/{id}/stream?static=true&…
// ohne Anmeldedaten. Sie gilt nur mit der PlaySessionId dieser Abfrage.
func TestExternalPlayer(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	e := newEnv(t)
	for name, pf := range map[string]string{"Hevc (2024)": "yuv420p", "Zehnbit (2024)": "yuv420p10le"} {
		dir := filepath.Join(e.dir, "filme", name)
		_ = os.MkdirAll(dir, 0o755)
		if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=24:duration=3",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:v", "libx265", "-pix_fmt", pf, "-x265-params", "log-level=error",
			"-c:a", "eac3", "-y", filepath.Join(dir, name+".mkv")).CombinedOutput(); err != nil {
			t.Skipf("Testvideo (libx265): %v %s", err, out)
		}
	}
	if _, err := e.s.Lib.Scan(e.s.Cfg().Folders); err != nil {
		t.Fatal(err)
	}
	e.s.FFprobe = library.FindFFprobe(ff)
	e.s.HLS = stream.NewHLSManager(ff, filepath.Join(e.dir, "hls"), 2)
	e.s.HLS.HWAccel = "off"
	defer e.s.HLS.StopAll()
	tok := e.login("anna")
	dash := func(id string) string {
		return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	}
	id := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=hevc", tok, nil))[0]["Id"].(string)
	did := dash(id)
	// DeviceProfileBuilder.getExternalPlayerProfile() der App 2.7.3
	sub := []any{}
	for _, f := range []string{"srt", "subrip", "ass", "ssa", "pgs", "pgssub", "dvdsub", "vtt", "sub", "idx"} {
		sub = append(sub, map[string]any{"Format": f, "Method": "Embed"}, map[string]any{"Format": f, "Method": "External"})
	}
	body := map[string]any{"MaxStreamingBitrate": 2147483647, "EnableDirectPlay": true, "EnableDirectStream": true, "EnableTranscoding": true,
		"AllowVideoStreamCopy": true, "AllowAudioStreamCopy": true, "AutoOpenLiveStream": false,
		"DeviceProfile": map[string]any{"Name": "Jellyfin Android External Player",
			"DirectPlayProfiles":  []any{map[string]any{"Type": "Video", "Container": ""}, map[string]any{"Type": "Audio", "Container": ""}},
			"TranscodingProfiles": []any{}, "ContainerProfiles": []any{}, "CodecProfiles": []any{}, "SubtitleProfiles": sub,
			"MaxStreamingBitrate": 2147483647, "MaxStaticBitrate": 2147483647}}
	pi := e.json("POST", "/Items/"+did+"/PlaybackInfo", tok, body)
	s0 := pi["MediaSources"].([]any)[0].(map[string]any)
	srcID := s0["Id"].(string)
	if s0["SupportsDirectPlay"] != true {
		t.Fatalf("externer Player: Direktwiedergabe erwartet: %v", s0)
	}
	psid := pi["PlaySessionId"].(string)
	get := func(item, ps string) (int, int64) {
		q := url.Values{"static": {"true"}, "mediaSourceId": {srcID}, "playSessionId": {ps}}
		req, _ := http.NewRequest("GET", e.ts.URL+"/Videos/"+item+"/stream?"+q.Encode(), nil)
		req.Header.Set("Range", "bytes=0-")
		req.Header.Set("User-Agent", "VLC/3.6.0 LibVLC/3.0.21")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, n
	}
	if c, n := get(did, psid); c != 206 || n != int64(s0["Size"].(float64)) {
		t.Fatalf("VLC: %d, %d Bytes", c, n)
	}
	if c, _ := get(did, "0123456789abcdef0123456789abcdef"); c != 401 {
		t.Fatalf("fremde PlaySessionId: %d", c)
	}
	other := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=Film%20A", tok, nil))[0]["Id"].(string)
	if c, _ := get(other, psid); c != 401 {
		t.Fatalf("anderer Titel mit PlaySessionId: %d", c)
	}

	// HEVC 10 Bit (bzw. über 1080p): wie bei der App „DS video“ wandelt der
	// Server um – der Abruf der Originaldatei wird auf HLS (H.264) umgeleitet.
	id10 := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=zehnbit", tok, nil))[0]["Id"].(string)
	pi = e.json("POST", "/Items/"+dash(id10)+"/PlaybackInfo", tok, body)
	psid, srcID = pi["PlaySessionId"].(string), pi["MediaSources"].([]any)[0].(map[string]any)["Id"].(string)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q := url.Values{"static": {"true"}, "mediaSourceId": {srcID}, "playSessionId": {psid}}
	req, _ := http.NewRequest("GET", e.ts.URL+"/Videos/"+dash(id10)+"/stream?"+q.Encode(), nil)
	req.Header.Set("User-Agent", "VLC/3.6.0 LibVLC/3.0.21")
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 302 || !strings.Contains(loc, "/master.m3u8?") || !strings.Contains(loc, "VideoCodec=h264") {
		t.Fatalf("Umleitung erwartet: %d %q", resp.StatusCode, loc)
	}
	// wie VLC: Liste, Segmentliste, erstes Segment
	get2 := func(u string) string {
		r, err := http.Get(u)
		if err != nil || r.StatusCode != 200 {
			t.Fatalf("%s: %v %v", u, err, r)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return string(b)
	}
	base, _ := url.Parse(e.ts.URL + loc)
	m := get2(base.String())
	var variant string
	for _, l := range strings.Split(m, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			variant = l
		}
	}
	vu, _ := base.Parse(variant)
	pl := get2(vu.String())
	var seg string
	for _, l := range strings.Split(pl, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			seg = l
			break
		}
	}
	su, _ := vu.Parse(seg)
	if b := get2(su.String()); len(b) < 1000 || !strings.HasSuffix(su.Path, ".m4s") {
		t.Fatalf("Segment %s: %d Bytes", su, len(b))
	}
	// Ein Player mit HLS-Unterstützung (hier FFmpeg wie in VLC/mpv) spielt die
	// Adresse der App samt Umleitung ab.
	if out, err := exec.Command(ff, "-v", "error", "-i", e.ts.URL+"/Videos/"+dash(id10)+"/stream?"+q.Encode(), "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("Wiedergabe über die Umleitung: %v %s", err, out)
	}
}

package jellyfin

import (
	"bufio"
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

// TestTranscodeHLS: Datei, die die App nicht direkt abspielt (MKV mit MPEG-4
// und MP3) → TranscodingUrl → master.m3u8 → HLS-Liste und Segmente.
func TestTranscodeHLS(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	e := newEnv(t)
	dir := filepath.Join(e.dir, "filme", "Clip (2022)")
	_ = os.MkdirAll(dir, 0o755)
	src := filepath.Join(dir, "Clip (2022).mkv")
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=14",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=14", "-c:v", "mpeg4", "-c:a", "libmp3lame", "-y", src).CombinedOutput(); err != nil {
		t.Skipf("Testvideo: %v %s", err, out)
	}
	if _, err := e.s.Lib.Scan(e.s.Cfg().Folders); err != nil {
		t.Fatal(err)
	}
	e.s.FFprobe = library.FindFFprobe(ff)
	if e.s.FFprobe == "" {
		e.s.FFprobe = ff
	}
	e.s.HLS = stream.NewHLSManager(ff, filepath.Join(e.dir, "hls"), 2)
	defer e.s.HLS.StopAll()
	cfg := *e.s.Cfg()
	cfg.HWAccel = "off"
	e.s.SetCfg(cfg)
	e.s.HLS.HWAccel = "off"

	tok := e.login("anna")
	clip := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=clip", tok, nil))
	if len(clip) != 1 {
		t.Fatalf("Clip: %v", names(clip))
	}
	id := clip[0]["Id"].(string)
	prof := map[string]any{"DeviceProfile": map[string]any{
		"DirectPlayProfiles":  []any{map[string]any{"Container": "mp4", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac"}},
		"TranscodingProfiles": []any{map[string]any{"Container": "ts", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac", "Protocol": "hls"}},
	}}
	pi := e.json("POST", "/Items/"+id+"/PlaybackInfo?StartTimeTicks=60000000", tok, prof)
	srcInfo := pi["MediaSources"].([]any)[0].(map[string]any)
	tu, _ := srcInfo["TranscodingUrl"].(string)
	if srcInfo["SupportsDirectPlay"] != false || tu == "" || srcInfo["TranscodingSubProtocol"] != "hls" {
		t.Fatalf("Umwandlung erwartet: %v", srcInfo)
	}
	if !strings.Contains(tu, "StartTimeTicks=60000000") || !strings.Contains(tu, "api_key=") {
		t.Fatalf("TranscodingUrl: %s", tu)
	}
	// Safari/AVPlayer fragen die Adresse zuerst in Teilbereichen ab (206) –
	// das darf keinen neuen Job starten (sonst ist die erste Liste tot).
	for _, rg := range []string{"bytes=0-1", "bytes=0-7"} {
		req, _ := http.NewRequest("GET", e.ts.URL+tu, nil)
		req.Header.Set("Range", rg)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("master %s: %v %v", rg, err, resp)
		}
		resp.Body.Close()
	}
	// Die App hängt die Adresse an ihre Server-Adresse an (ohne Token-Header).
	resp, err := http.Get(e.ts.URL + tu)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("master: %v %v", err, resp)
	}
	again := func() string {
		r, err := http.Get(e.ts.URL + tu)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b := new(strings.Builder)
		_, _ = io.Copy(b, r.Body)
		return b.String()
	}
	if a, b := again(), again(); a != b || e.s.HLS.Count() != 1 {
		t.Fatalf("mehrfache Abfrage startet neue Umwandlungen:\n%s\n%s", a, b)
	}
	var variant string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if l := sc.Text(); l != "" && !strings.HasPrefix(l, "#") {
			variant = l
		}
	}
	resp.Body.Close()
	base, _ := url.Parse(e.ts.URL + tu)
	vu, _ := base.Parse(variant)
	resp, err = http.Get(vu.String())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("Liste %s: %v %v", vu, err, resp)
	}
	var segs []string
	sc = bufio.NewScanner(resp.Body)
	body := ""
	for sc.Scan() {
		l := sc.Text()
		body += l + "\n"
		if l != "" && !strings.HasPrefix(l, "#") {
			segs = append(segs, l)
		}
	}
	resp.Body.Close()
	if len(segs) < 2 || !strings.Contains(body, "EXT-X-START:TIME-OFFSET=6") {
		t.Fatalf("Liste:\n%s", body)
	}
	for _, name := range []string{"init.mp4", segs[1]} {
		su, _ := vu.Parse(name)
		resp, err := http.Get(su.String())
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", name, err, resp)
		}
		resp.Body.Close()
	}
	// Ende der Wiedergabe beendet die Umwandlung
	q, _ := url.ParseQuery(base.RawQuery)
	e.do("POST", "/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": id, "PlaySessionId": q.Get("PlaySessionId"), "PositionTicks": 80000000})
	if n := e.s.HLS.Count(); n != 0 {
		t.Fatalf("Umwandlung läuft noch (%d)", n)
	}
}

// TestCodecProfiles: Bedingungen der App (z.B. H.264 höchstens 8 Bit) – sonst
// keine Direktwiedergabe und Umwandlung statt Umverpacken.
func TestCodecProfiles(t *testing.T) {
	hi10 := &library.Probe{OK: true, VideoCodec: "h264", VideoProfile: "High 10", PixFmt: "yuv420p10le", Width: 1920, Height: 1080}
	hi8 := &library.Probe{OK: true, VideoCodec: "h264", VideoProfile: "High", PixFmt: "yuv420p", Width: 1920, Height: 1080}
	hevc10 := &library.Probe{OK: true, VideoCodec: "hevc", VideoProfile: "Main 10", PixFmt: "yuv420p10le"}
	p := &deviceProfile{CodecProfiles: []codecProfile{{Type: "Video", Codec: "h264", Conditions: []profileCondition{
		{Condition: "LessThanEqual", Property: "VideoBitDepth", Value: "8"},
		{Condition: "EqualsAny", Property: "VideoProfile", Value: "high|main|baseline|constrained baseline"},
		{Condition: "LessThanEqual", Property: "Width", Value: "3840"},
		{Condition: "LessThanEqual", Property: "VideoLevel", Value: "52"}, // unbekannt → erfüllt
	}}}}
	if p.videoOK(hi10) || !p.videoOK(hi8) || !p.videoOK(hevc10) || !(*deviceProfile)(nil).videoOK(hi10) {
		t.Fatal("Bedingungen falsch ausgewertet")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	e := newEnv(t)
	dir := filepath.Join(e.dir, "filme", "Zehn (2020)")
	_ = os.MkdirAll(dir, 0o755)
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-c:v", "libx264", "-pix_fmt", "yuv420p10le", "-y", filepath.Join(dir, "Zehn (2020).mp4")).CombinedOutput(); err != nil {
		t.Skipf("Testvideo (libx264 10 Bit): %v %s", err, out)
	}
	if _, err := e.s.Lib.Scan(e.s.Cfg().Folders); err != nil {
		t.Fatal(err)
	}
	e.s.FFprobe = library.FindFFprobe(ff)
	if e.s.FFprobe == "" {
		e.s.FFprobe = ff
	}
	e.s.HLS = stream.NewHLSManager(ff, filepath.Join(e.dir, "hls"), 2)
	defer e.s.HLS.StopAll()
	tok := e.login("anna")
	it := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=zehn", tok, nil))
	if len(it) != 1 {
		t.Fatalf("Film: %v", names(it))
	}
	id := it[0]["Id"].(string)
	prof := map[string]any{
		"DirectPlayProfiles":  []any{map[string]any{"Container": "mp4", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac"}},
		"TranscodingProfiles": []any{map[string]any{"Container": "ts", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac", "Protocol": "hls"}},
	}
	src := func() map[string]any {
		pi := e.json("POST", "/Items/"+id+"/PlaybackInfo", tok, map[string]any{"DeviceProfile": prof})
		return pi["MediaSources"].([]any)[0].(map[string]any)
	}
	if s := src(); s["SupportsDirectPlay"] != true { // ohne Bedingungen: die App entscheidet (z.B. Infuse)
		t.Fatalf("ohne CodecProfiles direkt erwartet: %v", s["SupportsDirectPlay"])
	}
	prof["CodecProfiles"] = []any{map[string]any{"Type": "Video", "Codec": "h264", "Conditions": []any{
		map[string]any{"Condition": "LessThanEqual", "Property": "VideoBitDepth", "Value": "8"}}}}
	s := src()
	tu, _ := s["TranscodingUrl"].(string)
	if s["SupportsDirectPlay"] != false || !strings.Contains(tu, "AllowVideoStreamCopy=false") {
		t.Fatalf("10 Bit: Umwandlung erwartet: %v %s", s["SupportsDirectPlay"], tu)
	}
}

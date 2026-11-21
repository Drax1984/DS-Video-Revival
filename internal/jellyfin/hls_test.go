package jellyfin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// Übersicht wie bei Jellyfin: CODECS aus der Ausgabe, Auflösung, SDR
	if m := again(); !strings.Contains(m, `CODECS="avc1.`) || !strings.Contains(m, `,mp4a.40.2"`) || !strings.Contains(m, "RESOLUTION=320x240") ||
		!strings.Contains(m, "FRAME-RATE=25.000") || !strings.Contains(m, "VIDEO-RANGE=SDR") {
		t.Fatalf("master.m3u8:\n%s", m)
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
	// Die Mediathek nennt „ts“: MPEG-TS-Segmente ohne init.mp4 (wie Jellyfin)
	if !strings.HasSuffix(segs[1], ".ts") || strings.Contains(body, "EXT-X-MAP") {
		t.Fatalf("MPEG-TS erwartet:\n%.300s", body)
	}
	for _, name := range []string{segs[1]} {
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

// TestBitrateTest: /Playback/BitrateTest liefert die gewünschte Menge Daten
// (nur angemeldet).
func TestBitrateTest(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("GET", e.ts.URL+"/Playback/BitrateTest?size=300000", nil)
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ohne Anmeldung: %v %v", err, r)
	}
	tok := e.login("anna")
	req, _ = http.NewRequest("GET", e.ts.URL+"/Playback/BitrateTest?size=300000", nil)
	req.Header.Set("Authorization", `MediaBrowser Client="t", Device="t", DeviceId="t", Version="1", Token="`+tok+`"`)
	r, err := http.DefaultClient.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("%v %v", err, r)
	}
	n, _ := io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if n != 300000 {
		t.Fatalf("%d Bytes statt 300000", n)
	}
}

// TestMasterHEVC: 4K-Fall im Kleinen – HEVC mit HDR10 wird umverpackt (fMP4),
// die Übersicht nennt hvc1-Kennung, Auflösung und VIDEO-RANGE=PQ. Ohne diese
// Angaben spielt der Apple-Player auf dem iPhone nicht.
func TestMasterHEVC(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	e := newEnv(t)
	dir := filepath.Join(e.dir, "filme", "Hdr (2021)")
	_ = os.MkdirAll(dir, 0o755)
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=24:duration=14",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=14", "-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", "-c:a", "eac3", "-y", filepath.Join(dir, "Hdr (2021).mkv")).CombinedOutput(); err != nil {
		t.Skipf("Testvideo (libx265): %v %s", err, out)
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
	it := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=hdr", tok, nil))
	if len(it) != 1 {
		t.Fatalf("Film: %v", names(it))
	}
	prof := map[string]any{"DeviceProfile": map[string]any{
		"DirectPlayProfiles":  []any{map[string]any{"Container": "mp4", "Type": "Video", "VideoCodec": "h264,hevc", "AudioCodec": "aac,eac3"}},
		"TranscodingProfiles": []any{map[string]any{"Container": "ts", "Type": "Video", "VideoCodec": "h264,hevc", "AudioCodec": "aac,eac3", "Protocol": "hls"}},
	}}
	pi := e.json("POST", "/Items/"+it[0]["Id"].(string)+"/PlaybackInfo", tok, prof)
	tu, _ := pi["MediaSources"].([]any)[0].(map[string]any)["TranscodingUrl"].(string)
	if tu == "" {
		t.Fatalf("keine TranscodingUrl: %v", pi)
	}
	resp, err := http.Get(e.ts.URL + tu)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("master: %v %v", err, resp)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := string(b)
	if !strings.Contains(m, `CODECS="hvc1.2.4.L`) || !strings.Contains(m, `,ec-3"`) || !strings.Contains(m, "RESOLUTION=640x360") || !strings.Contains(m, "VIDEO-RANGE=PQ") {
		t.Fatalf("master.m3u8:\n%s", m)
	}
	// Segmentliste in fMP4 (Apple: HEVC nur so)
	var variant string
	for _, l := range strings.Split(m, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			variant = l
		}
	}
	base, _ := url.Parse(e.ts.URL + tu)
	vu, _ := base.Parse(variant)
	r2, err := http.Get(vu.String())
	if err != nil || r2.StatusCode != 200 {
		t.Fatalf("Liste: %v %v", err, r2)
	}
	pl, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if !strings.Contains(string(pl), "#EXT-X-MAP") || !strings.Contains(string(pl), ".m4s") {
		t.Fatalf("fMP4 erwartet:\n%.300s", pl)
	}
}

// TestAppleDownload: Herunterladen in der App „Jellyfin“ (iPhone). Die
// Mediathek bereitet MKV fürs iPhone vor (HEVC nur umverpackt, als „hvc1“,
// E-AC-3 bleibt). Fragt die App mit ihrem Download-Profil (ohne HLS), bekommt
// sie keine HLS-Adresse, sondern die fertige MP4-Datei als eigene Quelle. Die
// Übertragung wird gezählt, damit die Mediathek „auf dem iPhone“ anzeigen kann.
func TestAppleDownload(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	e := newEnv(t)
	dir := filepath.Join(e.dir, "filme", "Hevc (2024)")
	_ = os.MkdirAll(dir, 0o755)
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=24:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6", "-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error",
		"-c:a", "eac3", "-y", filepath.Join(dir, "Hevc (2024).mkv")).CombinedOutput(); err != nil {
		t.Skipf("Testvideo (libx265): %v %s", err, out)
	}
	if _, err := e.s.Lib.Scan(e.s.Cfg().Folders); err != nil {
		t.Fatal(err)
	}
	e.s.FFprobe = library.FindFFprobe(ff)
	e.s.HLS = stream.NewHLSManager(ff, filepath.Join(e.dir, "hls"), 2)
	e.s.HLS.HWAccel = "off"
	defer e.s.HLS.StopAll()
	e.s.Offline = stream.NewOfflineManager(e.s.HLS, filepath.Join(e.dir, "offline"))
	e.s.Offline.FFprobe = func() string { return e.s.FFprobe }
	defer e.s.Offline.Shutdown()
	tok := e.login("anna")
	it := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=hevc", tok, nil))
	if len(it) != 1 {
		t.Fatalf("Film: %v", names(it))
	}
	id := it[0]["Id"].(string)
	// Download-Profil der App: iOS-Profil ohne HLS (toDownloadProfile)
	prof := map[string]any{"DeviceProfile": map[string]any{
		"DirectPlayProfiles":  []any{map[string]any{"Container": "mp4,m4v", "Type": "Video", "VideoCodec": "hevc,h264", "AudioCodec": "aac,mp3,ac3,eac3,flac,alac"}},
		"TranscodingProfiles": []any{map[string]any{"Container": "mp4", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac,mp3,ac3,eac3,flac,alac", "Protocol": "http"}},
	}}
	srcs := e.json("POST", "/Items/"+id+"/PlaybackInfo", tok, prof)["MediaSources"].([]any)
	if s0 := srcs[0].(map[string]any); s0["SupportsDirectPlay"] == true || s0["TranscodingUrl"] != nil || s0["SupportsTranscoding"] == true {
		t.Fatalf("ohne Umwandlung: weder direkt noch HLS erwartet: %v", s0)
	}
	// Mediathek: fürs iPhone vorbereiten
	st := e.json("POST", "/DsvrOffline/"+id, tok, map[string]any{})
	for i := 0; i < 100 && st["State"] != "done"; i++ {
		time.Sleep(200 * time.Millisecond)
		st = e.json("GET", "/DsvrOffline/"+id, tok, nil)
	}
	if st["State"] != "done" || !strings.Contains(st["Mode"].(string), "Video kopiert") || !strings.Contains(st["Mode"].(string), "Ton kopiert") {
		t.Fatalf("Umverpacken erwartet: %v", st)
	}
	srcs = e.json("POST", "/Items/"+id+"/PlaybackInfo", tok, prof)["MediaSources"].([]any)
	s0 := srcs[0].(map[string]any)
	if s0["SupportsDirectPlay"] != true || s0["Container"] != "mp4" || !strings.HasPrefix(s0["Id"].(string), "dsvroff") {
		t.Fatalf("fertige MP4 als erste Quelle erwartet: %v", s0)
	}
	// So lädt die App (useDownloadHandler): /Videos/{id}/stream.mp4?Static=true&mediaSourceId=…&ApiKey=…
	resp, err := http.Get(e.ts.URL + "/Videos/" + id + "/stream.mp4?Static=true&mediaSourceId=" + s0["Id"].(string) + "&ApiKey=" + tok)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("Download: %v %v", err, resp)
	}
	out := filepath.Join(e.dir, "dl.mp4")
	fh, _ := os.Create(out)
	n, _ := io.Copy(fh, resp.Body)
	fh.Close()
	resp.Body.Close()
	if n != int64(s0["Size"].(float64)) {
		t.Fatalf("%d von %v Bytes", n, s0["Size"])
	}
	if fp := library.FindFFprobe(ff); fp != "" {
		b, _ := exec.Command(fp, "-v", "error", "-show_entries", "stream=codec_name,codec_tag_string", "-of", "csv=p=0", out).Output()
		if !strings.Contains(string(b), "hevc,hvc1") || !strings.Contains(string(b), "eac3") {
			t.Fatalf("MP4: %s", b)
		}
	}
	// Mediathek sieht die abgeschlossene Übertragung
	tr, _ := e.json("GET", "/DsvrOffline/"+id, tok, nil)["Transfer"].(map[string]any)
	if tr == nil || tr["Done"] != true || tr["Sent"] != tr["Total"] {
		t.Fatalf("Übertragung: %v", tr)
	}

	// Von unterwegs über DSM (Basis-Pfad /jellyfin): Für Direktwiedergabe baut
	// die App die Adresse ab der Wurzel („/Videos/…“) und verlöre /jellyfin –
	// daher dort eine „Umwandlung“ mit vollständigem Pfad.
	srv := httptest.NewServer(e.s)
	defer srv.Close()
	base := srv.URL + "/jellyfin"
	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		rq, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
		rq.Header.Set("Authorization", authHdr+`, Token="`+tok+`"`)
		rq.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	_, pi := post("/Items/"+id+"/PlaybackInfo", prof)
	s0 = pi["MediaSources"].([]any)[0].(map[string]any)
	tu, _ := s0["TranscodingUrl"].(string)
	if s0["SupportsDirectPlay"] != false || s0["SupportsDirectStream"] != false || s0["SupportsTranscoding"] != true ||
		!strings.HasPrefix(tu, "/jellyfin/Items/") || s0["TranscodingContainer"] != "mp4" || !strings.HasPrefix(pi["PlaySessionId"].(string), dlSessionPrefix) {
		t.Fatalf("über /jellyfin: Umwandlung mit vollem Pfad erwartet: %v", s0)
	}
	// wie die App: new URL(TranscodingUrl, Serveradresse) ohne weitere Kopfzeilen
	bu, _ := url.Parse(base)
	ru, _ := url.Parse(tu)
	resp, err = http.Get(bu.ResolveReference(ru).String())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("Download über /jellyfin: %v %v", err, resp)
	}
	n, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n != int64(s0["Size"].(float64)) {
		t.Fatalf("über /jellyfin: %d von %v Bytes", n, s0["Size"])
	}
	// Danach meldet die App „Wiedergabe beendet“ – keine Wiedergabe, Position bleibt.
	fid := 0
	all, _ := e.s.Lib.Items(library.Query{})
	for _, itx := range all {
		for _, f := range e.s.Lib.Files(itx) {
			if strings.HasSuffix(f.Path, "Hevc (2024).mkv") {
				fid = f.ID
			}
		}
	}
	_ = e.s.Lib.SetWatch("anna", fid, 120)
	if c, _ := post("/Sessions/Playing/Stopped", map[string]any{"ItemId": id, "PlaySessionId": pi["PlaySessionId"], "PositionTicks": 0}); c != 204 {
		t.Fatalf("Stopped: %d", c)
	}
	if w, _ := e.s.Lib.Watch("anna", fid); w.Position != 120 {
		t.Fatalf("Position nach dem Herunterladen verändert: %v", w)
	}
}

package vs

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dsvideorevival/internal/stream"
)

// TestOfflineConversion: MKV mit H.264 + E-AC-3 (wie „Rick and Morty …mkv“)
// → Aufgabe anlegen, Fortschritt abfragen, MP4 (H.264 kopiert, AAC-LC)
// herunterladen, Aufgabe löschen.
func TestOfflineConversion(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	fp, _ := exec.LookPath("ffprobe")
	ts, _, dir := newTestServer(t, "local")
	s := ts.Config.Handler.(*Server)
	src := filepath.Join(dir, "media", "Film C (2019).mkv")
	out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "eac3", "-y", src).CombinedOutput()
	if err != nil {
		t.Skipf("Testdatei nicht erzeugbar: %v %s", err, out)
	}
	if _, err := s.Lib.Scan(s.Cfg().Folders); err != nil {
		t.Fatal(err)
	}
	s.FFprobe = fp
	if s.FFprobe == "" {
		s.FFprobe = ff
	}
	s.HLS = stream.NewHLSManager(ff, filepath.Join(dir, "hls"), 1)
	s.Offline = stream.NewOfflineManager(s.HLS, filepath.Join(dir, "offline"))
	s.Offline.FFprobe = func() string { return s.FFprobe }
	sid := login(t, ts.URL)

	info := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Info"}, "version": {"1"}, "method": {"get"}, "_sid": {sid}})
	if p := info["data"].(map[string]any)["privilege"].(map[string]any); p["offline_conversion"] != true {
		t.Fatalf("privilege: %v", p)
	}

	m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}, "additional": {`["file"]`}})
	fid := ""
	for _, x := range m["data"].(map[string]any)["movie"].([]any) {
		mv := x.(map[string]any)
		if mv["title"] == "Film C" {
			fid = mv["additional"].(map[string]any)["file"].([]any)[0].(map[string]any)["id"].(string)
		}
	}
	if fid == "" {
		t.Fatalf("Film C nicht in der Liste: %v", m)
	}

	create := func() int {
		c := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.OfflineConversion"}, "version": {"1"}, "method": {"create"}, "_sid": {sid},
			"file_id": {fid}, "audio_id": {`"0"`}, "video_profile": {`"high"`}})
		if c["success"] != true {
			t.Fatalf("create: %v", c)
		}
		return int(c["data"].(map[string]any)["id"].(float64))
	}
	id := create()

	var task map[string]any
	deadline := time.Now().Add(60 * time.Second)
	for {
		l := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.OfflineConversion"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}, "additional": {`["produced_file"]`}})
		tasks := l["data"].(map[string]any)["task"].([]any)
		if len(tasks) != 1 {
			t.Fatalf("list: %v", l)
		}
		task = tasks[0].(map[string]any)
		st := task["status"].(map[string]any)["state"]
		if st == "done" {
			break
		}
		if st == "error" || time.Now().After(deadline) {
			tk, _ := s.Offline.Get(id)
			t.Fatalf("Umwandlung: %v (%s)", task, tk.ErrMsg)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if again := create(); again != id {
		t.Fatalf("zweites create: %d statt %d", again, id)
	}
	set := task["setting"].(map[string]any)
	add := set["additional"].(map[string]any)
	pf := add["produced_file"].(map[string]any)
	if pf["conversion_produced"] != true || pf["video_codec"] != "h264" || pf["audio_codec"] != "aac" || pf["container_type"] != "mp4" {
		t.Fatalf("produced_file: %v", pf)
	}
	if add["file"] == nil || add["video"] == nil || set["video_profile"] != "high" {
		t.Fatalf("setting: %v", set)
	}
	pid := pf["id"].(string)
	if n, _ := strconv.Atoi(pid); n != stream.ProducedBase+id {
		t.Fatalf("produced id %s", pid)
	}
	tk, _ := s.Offline.Get(id)
	if !strings.Contains(tk.Mode, "Video kopiert") || !strings.Contains(tk.Mode, "Ton → AAC") {
		t.Fatalf("Modus: %q", tk.Mode)
	}

	// Ablauf der iOS-App (Protokoll 0.5.21): list (done) → VS1 delete →
	// Subtitle list und File download mit der ID der erzeugten Datei.
	del := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.OfflineConversion"}, "version": {"1"}, "method": {"delete"}, "_sid": {sid}, "id": {"[" + strconv.Itoa(id) + "]"}})
	if del["success"] != true || len(del["data"].(map[string]any)["errors"].([]any)) != 0 {
		t.Fatalf("delete: %v", del)
	}
	l := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.OfflineConversion"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}})
	if n := len(l["data"].(map[string]any)["task"].([]any)); n != 0 {
		t.Fatalf("nach delete: %v", l)
	}
	sub := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.Subtitle"}, "version": {"2"}, "method": {"list"}, "_sid": {sid}, "id": {pid}})
	if sub["success"] != true {
		t.Fatalf("Subtitle list: %v", sub)
	}
	resp, err := http.Get(ts.URL + "/webapi/entry.cgi?api=SYNO.VideoStation2.File&version=2&method=download&id=" + pid + "&pin=%22%22&_sid=" + sid)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(b) < 12 || string(b[4:8]) != "ftyp" || !strings.Contains(resp.Header.Get("Content-Disposition"), "Film C (2019).mp4") {
		t.Fatalf("Download: %d %q %d Bytes", resp.StatusCode, resp.Header.Get("Content-Disposition"), len(b))
	}
	ti := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.File"}, "version": {"1"}, "method": {"get_track_info"}, "_sid": {sid}, "id": {pid}})
	d := ti["data"].(map[string]any)
	a := d["audio"].([]any)
	v := d["video"].([]any)
	if len(a) != 1 || a[0].(map[string]any)["codec"] != "aac" || a[0].(map[string]any)["profile"] != "LC" || len(v) != 1 || v[0].(map[string]any)["codec"] != "h264" {
		t.Fatalf("get_track_info: %v", d)
	}

	// Erneut angefordert: gleiche Aufgabe, wieder in der Liste.
	if again := create(); again != id {
		t.Fatalf("create nach delete: %d statt %d", again, id)
	}
	l = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.OfflineConversion"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}})
	if n := len(l["data"].(map[string]any)["task"].([]any)); n != 1 {
		t.Fatalf("nach erneutem create: %v", l)
	}

	// Unbekannte Datei: HTTP-Fehler (sonst speichert die App die Fehlermeldung als Video).
	resp, _ = http.Get(ts.URL + "/webapi/entry.cgi?api=SYNO.VideoStation2.File&version=2&method=download&id=" + strconv.Itoa(stream.ProducedBase+99) + "&_sid=" + sid)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Download unbekannt: %d", resp.StatusCode)
	}
	if _, err := os.Stat(tk.Out); err != nil {
		t.Fatal(err)
	}
}

// TestOfflineStopRestart: Stufe „low“ (Neukodierung), anhalten und neu starten.
func TestOfflineStopRestart(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "x.mkv")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=1280x720:rate=25:duration=20",
		"-c:v", "libx264", "-preset", "ultrafast", "-y", src).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	hls := stream.NewHLSManager(ff, filepath.Join(dir, "hls"), 1)
	hls.HWAccel = "off"
	m := stream.NewOfflineManager(hls, filepath.Join(dir, "offline"))
	tk, created, err := m.Create("anna", 7, "", "low", stream.OfflineSource{Path: src, Duration: 20, Height: 720, VideoCodec: "h264"})
	if err != nil || !created {
		t.Fatal(err)
	}
	if m.Stop("andere", tk.ID) {
		t.Fatal("fremde Aufgabe angehalten")
	}
	if !m.Stop("anna", tk.ID) {
		t.Fatal("Stop")
	}
	if g, _ := m.Get(tk.ID); g.State != stream.OfflineStop {
		t.Fatalf("Zustand %s", g.State)
	}
	m.Restart("anna", tk.ID)
	deadline := time.Now().Add(90 * time.Second)
	for {
		g, _ := m.Get(tk.ID)
		if g.State == stream.OfflineDone {
			if !strings.Contains(g.Mode, "≤480p") || g.OutProbe == nil && g.OutSize == 0 {
				t.Fatalf("fertig: %+v", g)
			}
			break
		}
		if g.State == stream.OfflineError || time.Now().After(deadline) {
			t.Fatalf("Aufgabe: %+v", g)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Neu laden: fertige Aufgabe bleibt erhalten
	m2 := stream.NewOfflineManager(hls, filepath.Join(dir, "offline"))
	if g, ok := m2.Get(tk.ID); !ok || g.State != stream.OfflineDone {
		t.Fatalf("nach Neustart: %+v", g)
	}
}

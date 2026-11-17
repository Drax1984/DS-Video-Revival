package stream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dsvideorevival/internal/library"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		path      string
		p         library.Probe
		caps      Caps
		mode      string
		copyAudio bool
	}{
		{"a.mp4", library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "aac"}, CapsBrowser, ModeDirect, true},
		{"a.mkv", library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "ac3"}, CapsBrowser, ModeRemux, false},
		{"a.mkv", library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "aac"}, CapsAndroid, ModeDirect, true},
		{"a.mkv", library.Probe{OK: true, VideoCodec: "hevc", AudioCodec: "eac3"}, CapsApple, ModeRemux, true},
		{"a.mkv", library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "ac3"}, CapsApple, ModeRemux, true},
		{"a.mkv", library.Probe{OK: true, VideoCodec: "hevc", AudioCodec: "aac"}, CapsBrowser, ModeTranscode, true},
		{"a.mp4", library.Probe{OK: true, VideoCodec: "hevc", AudioCodec: "aac"}, CapsApple, ModeDirect, true},
		{"a.avi", library.Probe{OK: true, VideoCodec: "mpeg4", AudioCodec: "mp3"}, CapsApple, ModeTranscode, false},
		{"a.ts", library.Probe{OK: true, VideoCodec: "mpeg2video", AudioCodec: "mp2"}, CapsBrowser, ModeTranscode, false},
		{"a.mkv", library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "dts"}, CapsApple, ModeRemux, false},
	}
	for _, c := range cases {
		d := Decide(&c.p, c.path, c.caps)
		if d.Mode != c.mode || d.CopyAudio != c.copyAudio {
			t.Errorf("%s %s/%s %s: %+v", c.path, c.p.VideoCodec, c.p.AudioCodec, c.caps.Name, d)
		}
	}
}

func TestHLSRemuxAndTranscode(t *testing.T) {
	ff := FindFFmpeg("")
	if ff == "" {
		t.Skip("kein ffmpeg")
	}
	dir := t.TempDir()
	mkv := filepath.Join(dir, "film.mkv")
	avi := filepath.Join(dir, "alt.avi")
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-f", "lavfi", "-i", "sine=frequency=440",
		"-t", "8", "-c:v", "libx264", "-g", "25", "-c:a", "ac3", mkv).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-f", "lavfi", "-i", "sine=frequency=440",
		"-t", "4", "-c:v", "mpeg4", "-c:a", "libmp3lame", avi).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	m := NewHLSManager(ff, filepath.Join(dir, "hls"), 2)
	m.HWAccel = "off"
	for _, tc := range []struct {
		src   string
		d     Decision
		audio string
	}{
		{mkv, Decide(&library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "ac3"}, mkv, CapsBrowser), "aac"},
		{mkv, Decide(&library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "ac3"}, mkv, CapsApple), "ac3"}, // iPhone: reiner Remux, kein Encoder nötig
		{avi, Decide(&library.Probe{OK: true, VideoCodec: "mpeg4", AudioCodec: "mp3"}, avi, CapsBrowser), "aac"},
	} {
		j, err := m.Start("anna", tc.src, tc.d, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.ServeFile(w, r, j, strings.TrimPrefix(r.URL.Path, "/hls/"+j.ID+"/"), "/hls/"+j.ID+"/")
		}))
		resp, err := http.Get(srv.URL + "/hls/" + j.ID + "/index.m3u8")
		if err != nil {
			t.Fatal(err)
		}
		pl, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" || !strings.Contains(string(pl), `#EXT-X-MAP:URI="/hls/`+j.ID+`/init.mp4"`) || !strings.Contains(string(pl), "/hls/"+j.ID+"/seg00000.m4s") {
			t.Fatalf("Playlist (%s):\n%s", tc.d.Mode, pl)
		}
		seg, err := http.Get(srv.URL + "/hls/" + j.ID + "/init.mp4")
		if err != nil || seg.StatusCode != 200 {
			t.Fatalf("init.mp4: %v %v", err, seg.StatusCode)
		}
		b, _ := io.ReadAll(seg.Body)
		seg.Body.Close()
		if len(b) < 8 || string(b[4:8]) != "ftyp" {
			t.Errorf("init.mp4 kein fMP4")
		}
		<-j.done
		if j.err != nil {
			t.Errorf("%s: %v", tc.d.Mode, j.err)
		}
		// Codecs des Ergebnisses prüfen
		out, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", filepath.Join(j.Dir, "index.m3u8")).Output()
		if got := strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(out)), ",", " ")); len(got) < 2 || got[0] != "h264" || got[1] != tc.audio {
			t.Errorf("%s: Ergebnis-Codecs %v", tc.d.Mode, got)
		}
		srv.Close()
		m.Stop(j.ID)
	}
}

// FFmpeg ohne fMP4-HLS (eingeschränkter Build): Rückfall auf MPEG-TS-Segmente.
func TestHLSFallbackTS(t *testing.T) {
	ff := FindFFmpeg("")
	if ff == "" {
		t.Skip("kein ffmpeg")
	}
	dir := t.TempDir()
	wrap := filepath.Join(dir, "ffmpeg")
	os.WriteFile(wrap, []byte("#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = fmp4 ] && { echo \"Unrecognized option hls_segment_type\" >&2; exit 1; }; done\nexec "+ff+" \"$@\"\n"), 0o755)
	mkv := filepath.Join(dir, "film.mkv")
	if out, err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-f", "lavfi", "-i", "sine=frequency=440",
		"-t", "4", "-c:v", "libx264", "-g", "25", "-c:a", "ac3", mkv).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	m := NewHLSManager(wrap, filepath.Join(dir, "hls"), 2)
	m.HWAccel = "off"
	j, err := m.Start("anna", mkv, Decide(&library.Probe{OK: true, VideoCodec: "h264", AudioCodec: "ac3"}, mkv, CapsApple), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !j.TS {
		t.Fatal("Rückfall auf MPEG-TS erwartet")
	}
	<-j.done
	if j.err != nil {
		t.Fatal(j.err)
	}
	rec := httptest.NewRecorder()
	m.ServeFile(rec, httptest.NewRequest("GET", "/hls/x/index.m3u8", nil), j, "index.m3u8", "/hls/"+j.ID+"/")
	if !strings.HasPrefix(rec.Body.String(), "#EXTM3U\n#EXT-X-START:TIME-OFFSET=0,PRECISE=YES\n") {
		t.Fatalf("Startpunkt fehlt:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/hls/"+j.ID+"/seg00000.ts") || strings.Contains(rec.Body.String(), "EXT-X-MAP") {
		t.Fatalf("Playlist:\n%s", rec.Body.String())
	}
	out, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", filepath.Join(j.Dir, "seg00000.ts")).Output()
	if s := strings.ReplaceAll(string(out), ",", ""); !strings.Contains(s, "h264") || !strings.Contains(s, "ac3") {
		t.Fatalf("Segment-Codecs: %q", out)
	}
	m.Stop(j.ID)
}

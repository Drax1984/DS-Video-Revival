package stream

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Durchsuchbare HLS-Liste (VOD): ganze Laufzeit sofort, Sprung an eine
// entfernte Stelle startet FFmpeg dort neu, Zeitstempel bleiben absolut.
// DSVR_TEST_FFMPEG erlaubt einen anderen FFmpeg (z.B. 4.x wie auf Synology).
func TestVODSeek(t *testing.T) {
	ff := os.Getenv("DSVR_TEST_FFMPEG")
	if ff == "" {
		ff = FindFFmpeg("")
	}
	fp, err := exec.LookPath("ffprobe")
	if ff == "" || err != nil {
		t.Skip("ffmpeg/ffprobe fehlt")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "film.mkv")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=sample_rate=48000",
		"-t", "420", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-pix_fmt", "yuv420p", "-c:a", "ac3", src).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	for _, tc := range []struct {
		name string
		copy bool
	}{{"Umverpacken", true}, {"Umwandeln", false}} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewHLSManager(ff, filepath.Join(dir, "hls-"+tc.name), 2)
			m.HWAccel = "off"
			d := Decision{Mode: ModeRemux, CopyVideo: true, CopyAudio: true, Client: "apple", Duration: 420}
			if !tc.copy {
				if !m.CanEncodeH264() {
					t.Skip("kein H.264-Encoder")
				}
				d = Decision{Mode: ModeTranscode, CopyAudio: true, MaxHeight: 180, Client: "apple", Duration: 420}
			}
			j, err := m.Start("anna", src, d, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Stop(j.ID)
			if !j.VOD {
				t.Fatal("VOD erwartet")
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				m.ServeFile(w, r, j, strings.TrimPrefix(r.URL.Path, "/hls/x/"), "/hls/x/")
			}))
			defer srv.Close()
			get := func(name string) []byte {
				t.Helper()
				resp, err := http.Get(srv.URL + "/hls/x/" + name)
				if err != nil || resp.StatusCode != 200 {
					t.Fatalf("%s: %v %v", name, err, resp)
				}
				defer resp.Body.Close()
				b := new(strings.Builder)
				buf := make([]byte, 1<<16)
				var out []byte
				for {
					n, err := resp.Body.Read(buf)
					out = append(out, buf[:n]...)
					if err != nil {
						break
					}
				}
				_ = b
				return out
			}
			pl := string(get("index.m3u8"))
			if !strings.Contains(pl, "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(pl, "#EXT-X-ENDLIST") || strings.Count(pl, "#EXTINF") != 70 {
				t.Fatalf("Liste:\n%.400s", pl)
			}
			init := get("init.mp4")
			first := func(seg []byte, typ string) float64 {
				p := filepath.Join(dir, "probe.mp4")
				_ = os.WriteFile(p, append(append([]byte{}, init...), seg...), 0o644)
				out, _ := exec.Command(fp, "-v", "error", "-select_streams", typ, "-show_entries", "packet=pts_time", "-of", "csv=p=0", p).Output()
				f := strings.Fields(strings.ReplaceAll(string(out), ",", " "))
				if len(f) == 0 {
					t.Fatalf("Segment ohne Spur %s", typ)
				}
				v, _ := strconv.ParseFloat(f[0], 64)
				return v
			}
			// Bild und Ton müssen in jedem Segment zusammenpassen.
			firstPTS := func(seg []byte) float64 {
				v, a := first(seg, "v"), first(seg, "a")
				if a-v > 0.3 || v-a > 0.3 {
					t.Logf("Spurbeginn Ton %.3f / Bild %.3f", a, v)
					t.Fatalf("Ton %.3f / Bild %.3f auseinander", a, v)
				}
				return v
			}
			if v := firstPTS(get("seg00000.m4s")); v > 0.5 {
				t.Fatalf("Segment 0 beginnt bei %.2f", v)
			}
			start := time.Now()
			v := firstPTS(get("seg00060.m4s")) // 6:00 – weit hinter dem Fortschritt → Neustart
			if v < 354 || v > 362 {
				t.Fatalf("Segment 60 beginnt bei %.2f (erwartet ~360)", v)
			}
			t.Logf("Sprung auf 6:00 in %v, Segment beginnt bei %.2f s", time.Since(start).Round(time.Millisecond), v)
			if v := firstPTS(get("seg00061.m4s")); v < 360 || v > 368 {
				t.Fatalf("Segment 61 beginnt bei %.2f", v)
			}
			if v := firstPTS(get("seg00010.m4s")); v < 54 || v > 62 { // zurück an den Anfang
				t.Fatalf("Segment 10 beginnt bei %.2f", v)
			}

			// Start mitten im Film (Fortsetzen), danach zurück an den Anfang:
			// die init.mp4 des ersten Laufs gilt für alle Läufe.
			j2, err := m.Start("anna", src, d, 300, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Stop(j2.ID)
			j = j2
			if pl := string(get("index.m3u8")); !strings.Contains(pl, "#EXT-X-START:TIME-OFFSET=300.000,PRECISE=YES") || strings.Count(pl, "#EXTINF") != 70 {
				t.Fatalf("Fortsetzen: Liste ohne Startstelle:\n%.400s", pl)
			}
			init = get("init.mp4")
			if v := firstPTS(get("seg00050.m4s")); v < 294 || v > 302 {
				t.Fatalf("Fortsetzen: Segment 50 beginnt bei %.2f", v)
			}
			if v := firstPTS(get("seg00001.m4s")); v < 4 || v > 8 {
				t.Fatalf("Fortsetzen: Segment 1 beginnt bei %.2f", v)
			}
			if v := firstPTS(get("seg00051.m4s")); v < 300 || v > 308 {
				t.Fatalf("Fortsetzen: Segment 51 beginnt bei %.2f", v)
			}
		})
	}
}

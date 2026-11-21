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
		ts   bool
	}{{"Umverpacken", true, false}, {"Umwandeln", false, false}, {"Umverpacken-TS", true, true}, {"Umwandeln-TS", false, true}} {
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
			d.TS = tc.ts
			j, err := m.Start("anna", src, d, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Stop(j.ID)
			if !j.VOD {
				t.Fatal("VOD erwartet")
			}
			L := j.segLen() // Umverpacken 6 s, Umwandeln 3 s
			seg := func(sec float64) string { return j.segName(int(sec / L)) }
			tsOff := 0.0 // MPEG-TS: FFmpeg beginnt bei 1,4 s
			if tc.ts {
				tsOff = 1.4
			}
			nSeg := strconv.Itoa(int(420 / L))
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
			if !strings.Contains(pl, "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(pl, "#EXT-X-ENDLIST") || strconv.Itoa(strings.Count(pl, "#EXTINF")) != nSeg {
				t.Fatalf("Liste:\n%.400s", pl)
			}
			var init []byte
			if !tc.ts {
				init = get("init.mp4")
			} else if strings.Contains(pl, "#EXT-X-MAP") {
				t.Fatal("MPEG-TS-Liste mit init.mp4")
			}
			first := func(seg []byte, typ string) float64 {
				p := filepath.Join(dir, "probe.mp4")
				_ = os.WriteFile(p, append(append([]byte{}, init...), seg...), 0o644)
				out, _ := exec.Command(fp, "-v", "error", "-select_streams", typ, "-show_entries", "packet=pts_time", "-of", "csv=p=0", p).Output()
				f := strings.Fields(strings.ReplaceAll(string(out), ",", " "))
				if len(f) == 0 {
					t.Fatalf("Segment ohne Spur %s", typ)
				}
				v, _ := strconv.ParseFloat(f[0], 64)
				return v - tsOff
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
			if v := firstPTS(get(j.segName(0))); v > 0.5 {
				t.Fatalf("Segment 0 beginnt bei %.2f", v)
			}
			start := time.Now()
			v := firstPTS(get(seg(360))) // 6:00 – weit hinter dem Fortschritt → Neustart
			if v < 354 || v > 362 {
				t.Fatalf("Segment bei 6:00 beginnt bei %.2f (erwartet ~360)", v)
			}
			t.Logf("Sprung auf 6:00 in %v, Segment beginnt bei %.2f s", time.Since(start).Round(time.Millisecond), v)
			if v := firstPTS(get(seg(360 + L))); v < 360 || v > 362+L {
				t.Fatalf("Segment danach beginnt bei %.2f", v)
			}
			if v := firstPTS(get(seg(60))); v < 54 || v > 62 { // zurück an den Anfang
				t.Fatalf("Segment bei 1:00 beginnt bei %.2f", v)
			}

			// Start mitten im Film (Fortsetzen), danach zurück an den Anfang:
			// die init.mp4 des ersten Laufs gilt für alle Läufe.
			j2, err := m.Start("anna", src, d, 300, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Stop(j2.ID)
			j = j2
			if pl := string(get("index.m3u8")); !strings.Contains(pl, "#EXT-X-START:TIME-OFFSET=300.000,PRECISE=YES") || strconv.Itoa(strings.Count(pl, "#EXTINF")) != nSeg {
				t.Fatalf("Fortsetzen: Liste ohne Startstelle:\n%.400s", pl)
			}
			if !tc.ts {
				init = get("init.mp4")
			}
			if v := firstPTS(get(seg(300))); v < 294 || v > 302 {
				t.Fatalf("Fortsetzen: Segment bei 5:00 beginnt bei %.2f", v)
			}
			if v := firstPTS(get(seg(L))); v < L-2 || v > L+2 {
				t.Fatalf("Fortsetzen: Segment 1 beginnt bei %.2f", v)
			}
			if v := firstPTS(get(seg(300 + L))); v < 300 || v > 302+L {
				t.Fatalf("Fortsetzen: Segment danach beginnt bei %.2f", v)
			}
		})
	}
}

// TestVODPacing: FFmpeg läuft der Wiedergabe nur begrenzt voraus (angehalten),
// holt auf, wenn der Player weiterliest, und alte Segmente werden gelöscht.
func TestVODPacing(t *testing.T) {
	ff := FindFFmpeg("")
	if ff == "" {
		t.Skip("ffmpeg fehlt")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "lang.mkv")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=sample_rate=48000",
		"-t", "1200", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-pix_fmt", "yuv420p", "-c:a", "aac", src).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	m := NewHLSManager(ff, filepath.Join(dir, "hls"), 2)
	m.HWAccel = "off"
	j, err := m.Start("anna", src, Decision{Mode: ModeRemux, CopyVideo: true, CopyAudio: true, Client: "apple", Duration: 1200, TS: true}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop(j.ID)
	get := func(k int) {
		t.Helper()
		rec := httptest.NewRecorder()
		m.ServeFile(rec, httptest.NewRequest("GET", "/x/"+j.segName(k), nil), j, j.segName(k), "/x/")
		if rec.Code != 200 {
			t.Fatalf("Segment %d: %d", k, rec.Code)
		}
	}
	get(0)
	ahead := int(paceAheadCopy / SegLen)
	wait := func(cond func() bool) bool {
		for i := 0; i < 100; i++ {
			m.pace(j)
			if cond() {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	paused := func() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.paused }
	if !wait(paused) {
		t.Fatalf("FFmpeg nicht angehalten (Stand %d)", j.headOf(0))
	}
	// Angehalten: Stand wächst nicht weiter, Film nicht ganz umgewandelt
	h1 := j.headOf(0)
	time.Sleep(700 * time.Millisecond)
	if h2 := j.headOf(0); h2 != h1 || h1 < ahead || h1 >= j.segCount()-1 || j.finished() {
		t.Fatalf("Vorlauf %d→%d Segmente (mindestens %d, Ende %d)", h1, h2, ahead, j.segCount())
	}
	// Player holt auf → FFmpeg läuft wieder bis zum neuen Vorlauf (zweimal),
	// alte Segmente verschwinden
	k := h1
	for step := 0; step < 2; step++ {
		k = j.headOf(0) - 2
		get(k)
		if !wait(func() bool { return j.headOf(0) >= k+ahead && paused() }) {
			t.Fatalf("kein Aufholen: Stand %d, gewünscht ab %d", j.headOf(0), k+ahead)
		}
	}
	keep := int(keepBehind / SegLen)
	if fileExists(j.genFile(0, j.segName(0))) || fileExists(j.genFile(0, j.segName(k-keep-1))) || !fileExists(j.genFile(0, j.segName(k-keep))) {
		t.Fatalf("alte Segmente nicht (richtig) gelöscht (Stelle %d)", k)
	}
}

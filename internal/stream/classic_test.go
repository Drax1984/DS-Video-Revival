package stream

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestClassicDSVideo: Umwandlungen für die DS-video-Apps laufen wie bis 0.6.6
// (6-s-Segmente, nur verkleinern, keine SDR-Kennzeichnung, keine Drosselung).
func TestClassicDSVideo(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "hdr.mkv")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=1280x720:rate=24:duration=12",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-color_primaries", "bt2020", "-color_trc", "smpte2084",
		"-colorspace", "bt2020nc", "-y", src).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	m := NewHLSManager(ff, filepath.Join(dir, "hls"), 4)
	m.HWAccel = "off"
	defer m.StopAll()
	args := func(classic bool) []string {
		j, err := m.Start("anna", src, Decision{Mode: ModeTranscode, MaxHeight: 480, Duration: 12, Client: "apple", Classic: classic}, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		if classic && j.segLen() != SegLen {
			t.Fatalf("Segmentlänge %v", j.segLen())
		}
		return j.cmd.Args
	}
	a := strings.Join(args(true), " ")
	if !strings.Contains(a, "-vf scale=-2:'min(ih,480)' ") || strings.Contains(a, "-color_primaries") || !strings.Contains(a, "-hls_time 6 ") {
		t.Fatalf("DS video: %s", a)
	}
	if b := args(false); !slices.Contains(b, "-color_primaries") {
		t.Fatalf("Jellyfin: %v", b)
	}
	// 4K-Quelle (DS video): schnelle Variante
	j, err := m.Start("anna", src, Decision{Mode: ModeTranscode, MaxHeight: 720, Duration: 12, Client: "apple", Classic: true, Fast4K: true}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	j.mu.Lock()
	f := strings.Join(j.cmd.Args, " ")
	j.mu.Unlock()
	if !strings.Contains(f, "-skip_loop_filter all -i ") || !strings.Contains(f, ":flags=fast_bilinear") || !strings.Contains(f, "-preset ultrafast") || !strings.Contains(f, "-hls_time 3 ") {
		t.Fatalf("4K schnell: %s", f)
	}
}

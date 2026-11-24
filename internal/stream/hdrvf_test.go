package stream

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSoftwareVFHDR: Tonemapping nur bis 1440p – bei 4K-HDR fiel die
// Software-Umwandlung auf der NAS sonst unter Echtzeit.
func TestSoftwareVFHDR(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg fehlt")
	}
	dir := t.TempDir()
	mk := func(name, size string) string {
		p := filepath.Join(dir, name)
		if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size="+size+":rate=24:duration=0.2",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-color_primaries", "bt2020", "-color_trc", "smpte2084",
			"-colorspace", "bt2020nc", "-y", p).CombinedOutput(); err != nil {
			t.Skipf("%v %s", err, out)
		}
		return p
	}
	m := NewHLSManager(ff, filepath.Join(dir, "hls"), 1)
	if !m.canTonemap() {
		t.Skip("FFmpeg ohne zscale/tonemap")
	}
	if vf := m.softwareVF(mk("hd.mkv", "1920x1080"), "1080"); !strings.Contains(vf, "tonemap") {
		t.Fatalf("1080p-HDR ohne Tonemapping: %s", vf)
	}
	if vf := m.softwareVF(mk("uhd.mkv", "3840x2160"), "720"); vf != "scale=-2:'min(ih,720)'" {
		t.Fatalf("4K-HDR: %s", vf)
	}
}

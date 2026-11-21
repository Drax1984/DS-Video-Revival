package stream

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// TestVideoCodecTag: Kennung für CODECS aus der echten Ausgabe – fMP4
// (init.mp4, HEVC und H.264) und MPEG-TS (SPS im ersten Segment).
func TestVideoCodecTag(t *testing.T) {
	ff := FindFFmpeg("")
	if ff == "" {
		t.Skip("ffmpeg fehlt")
	}
	dir := t.TempDir()
	mk := func(name string, args ...string) string {
		p := filepath.Join(dir, name)
		a := append([]string{"-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25", "-f", "lavfi", "-i", "sine=sample_rate=48000", "-t", "8"}, args...)
		if out, err := exec.Command("ffmpeg", append(a, "-y", p)...).CombinedOutput(); err != nil {
			t.Skipf("%s: %s", name, out)
		}
		return p
	}
	h264 := mk("h264.mkv", "-c:v", "libx264", "-profile:v", "high", "-level", "4.0", "-pix_fmt", "yuv420p", "-c:a", "aac")
	hevc := mk("hevc.mkv", "-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error", "-c:a", "eac3")
	m := NewHLSManager(ff, filepath.Join(dir, "hls"), 2)
	m.HWAccel = "off"
	for _, tc := range []struct {
		src  string
		d    Decision
		want string
	}{
		{h264, Decision{Mode: ModeRemux, CopyVideo: true, CopyAudio: true, Client: "apple", Duration: 8, TS: true}, `^avc1\.640028$`},
		{h264, Decision{Mode: ModeRemux, CopyVideo: true, CopyAudio: true, Client: "apple", Duration: 8}, `^avc1\.640028$`},
		{hevc, Decision{Mode: ModeRemux, CopyVideo: true, CopyAudio: true, HEVC: true, Client: "apple", Duration: 8}, `^hvc1\.2\.4\.L\d+(\.[0-9A-F]+)*$`},
	} {
		j, err := m.Start("anna", tc.src, tc.d, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := j.VideoCodecTag(15 * time.Second)
		m.Stop(j.ID)
		if !regexp.MustCompile(tc.want).MatchString(got) {
			t.Errorf("%s TS=%v: %q", filepath.Base(tc.src), tc.d.TS, got)
		}
	}
	if a := m.AudioCodecTag("eac3", true, "apple"); a != "ec-3" {
		t.Errorf("E-AC-3: %q", a)
	}
	if a := m.AudioCodecTag("dts", false, "apple"); a != "mp4a.40.2" {
		t.Errorf("DTS → AAC: %q", a)
	}
}

// hevcTag nach dem Beispiel aus ISO/IEC 14496-15 (Main 10, Level 5.1).
func TestHevcTag(t *testing.T) {
	p := []byte{1, 0x02, 0x20, 0, 0, 0, 0x90, 0, 0, 0, 0, 0, 153}
	if s := hevcTag("hvc1", p); s != "hvc1.2.4.L153.90" {
		t.Fatal(s)
	}
	p[1] = 0x22 // Tier High
	if s := hevcTag("hvc1", p); s != "hvc1.2.4.H153.90" {
		t.Fatal(s)
	}
}

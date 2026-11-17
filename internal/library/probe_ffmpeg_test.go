package library

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Ausgabe im Stil von ffmpeg 4.1 (Synology CodecPack „ffmpeg41“).
const ffmpeg41Out = `Input #0, matroska,webm, from '/volume1/video/Testfilm (2022)/Testfilm.2022.mkv':
  Metadata:
    encoder         : libebml v1.4.2 + libmatroska v1.6.4
  Duration: 01:44:12.35, start: 0.000000, bitrate: 10432 kb/s
    Stream #0:0(ger): Video: h264 (High), yuv420p(progressive), 1920x800 [SAR 1:1 DAR 12:5], 23.98 fps, 23.98 tbr, 1k tbn, 47.95 tbc (default)
    Metadata:
      BPS-eng         : 9783211
    Stream #0:1(ger): Audio: ac3, 48000 Hz, 5.1(side), fltp, 640 kb/s (default)
    Metadata:
      title           : Deutsch AC3
    Stream #0:2(eng): Audio: dts (DTS-HD MA), 48000 Hz, 7.1, s32p (24 bit)
    Stream #0:3(ger): Subtitle: subrip
    Metadata:
      title           : Forced
    Stream #0:4: Video: mjpeg (Baseline), yuvj420p(pc, bt470bg/unknown/unknown), 600x882, 90k tbr, 90k tbn, 90k tbc (attached pic)
At least one output file must be specified
`

func TestParseFFmpegInfo(t *testing.T) {
	p := parseFFmpegInfo(ffmpeg41Out, &Probe{Container: "mkv"})
	if !p.OK || p.VideoCodec != "h264" || p.Width != 1920 || p.Height != 800 || p.AudioCodec != "ac3" || p.Channels != 6 {
		t.Fatalf("%+v", p)
	}
	if int(p.Duration) != 6252 || p.Bitrate != 10432000 || p.FrameRate < 23.9 || p.AudioBitrate != 640000 {
		t.Fatalf("Dauer/Bitrate: %+v", p)
	}
	if len(p.AudioTracks) != 2 || p.AudioTracks[0].Title != "Deutsch AC3" || p.AudioTracks[1].Codec != "dts" || p.AudioTracks[1].Channels != 8 || p.AudioTracks[1].Language != "eng" {
		t.Fatalf("Tonspuren: %+v", p.AudioTracks)
	}
	if len(p.Subtitles) != 1 || p.Subtitles[0].Title != "Forced" || p.Subtitles[0].Codec != "subrip" {
		t.Fatalf("Untertitel: %+v", p.Subtitles)
	}
}

// Gegenprobe mit dem echten ffmpeg (falls vorhanden): gleiche Kernwerte wie ffprobe.
func TestFFmpegProbeMatchesFFprobe(t *testing.T) {
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg/ffprobe nicht installiert")
	}
	f := filepath.Join(t.TempDir(), "t.mkv")
	if err := exec.Command(ff, "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=25", "-f", "lavfi", "-i", "sine=sample_rate=48000",
		"-t", "2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "ac3", "-ac", "6", "-metadata:s:a:0", "language=ger", f).Run(); err != nil {
		t.Skip("Testdatei nicht erzeugbar:", err)
	}
	a, b := RunProbe(ff, f), RunProbe(fp, f)
	if !a.OK || a.VideoCodec != b.VideoCodec || a.AudioCodec != b.AudioCodec || a.Width != b.Width || a.Channels != b.Channels || a.AudioTracks[0].Language != "ger" {
		t.Fatalf("ffmpeg %+v\nffprobe %+v", a, b)
	}
}

func TestParseFFmpegProfiles(t *testing.T) {
	out := `Input #0, matroska,webm, from 'x.mkv':
  Duration: 00:22:13.00, start: 0.000000, bitrate: 2500 kb/s
  Stream #0:0: Video: h264 (High 10), yuv420p10le(tv, bt709, progressive), 1920x1080, SAR 1:1 DAR 16:9, 23.98 fps
  Stream #0:1(ger): Audio: aac (LC), 48000 Hz, stereo, fltp (default)
  Stream #0:2(eng): Audio: eac3, 48000 Hz, 5.1(side), fltp
  Stream #0:3(eng): Video: hevc (Main 10) (hvc1 / 0x31637668), yuv420p10le(tv), 3840x2160`
	p := parseFFmpegInfo(out, &Probe{})
	if p.VideoProfile != "High 10" || p.PixFmt != "yuv420p10le" || p.Version != ProbeVersion {
		t.Fatalf("Video: %+v", p)
	}
	if len(p.AudioTracks) != 2 || p.AudioTracks[0].Profile != "LC" || p.AudioTracks[1].Profile != "" {
		t.Fatalf("Audio: %+v", p.AudioTracks)
	}
}

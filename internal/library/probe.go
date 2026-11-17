package library

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Probe holds technical data of a video file (ermittelt per ffprobe).
type Probe struct {
	Container  string  `json:"container"`
	Duration   float64 `json:"duration"` // Sekunden
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	VideoCodec string  `json:"video_codec"`
	AudioCodec string  `json:"audio_codec"`
	Bitrate    int64   `json:"bitrate"`
	FrameRate  float64 `json:"frame_rate"`
	OK         bool    `json:"ok"`

	VideoBitrate int64         `json:"video_bitrate,omitempty"`
	AudioBitrate int64         `json:"audio_bitrate,omitempty"`
	Channels     int           `json:"channels,omitempty"`
	AudioTracks  []StreamTrack `json:"audio_tracks,omitempty"`
	Subtitles    []StreamTrack `json:"subtitles,omitempty"`
	Tool         string        `json:"tool,omitempty"`          // womit analysiert (für Wiederholung nach Werkzeugwechsel)
	VideoProfile string        `json:"video_profile,omitempty"` // z.B. „High“, „High 10“, „Main 10“
	PixFmt       string        `json:"pix_fmt,omitempty"`
	Version      int           `json:"v,omitempty"` // Stand der Analyse (ProbeVersion)
}

// ProbeVersion: ab 2 mit Profilen (Video/Audio) – ältere Analysen werden bei
// Bedarf wiederholt (z.B. für SYNO.VideoStation2.File get_track_info).
const ProbeVersion = 2

// NeedsProbe: noch nicht oder erfolglos mit einem anderen Werkzeug analysiert.
func (p *Probe) NeedsProbe(tool string) bool {
	return p == nil || (!p.OK && tool != "" && p.Tool != tool)
}

// NeedsProfiles: Analyse stammt aus einer Version ohne Profilangaben.
func (p *Probe) NeedsProfiles(tool string) bool {
	return p == nil || (tool != "" && p.Version < ProbeVersion)
}

// StreamTrack is an audio or subtitle stream inside the file.
type StreamTrack struct {
	Index    int    `json:"index"` // relative Nummer (0 = erste Spur dieses Typs)
	Codec    string `json:"codec"`
	Channels int    `json:"channels,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Profile  string `json:"profile,omitempty"` // Audio z.B. „LC“, „HE-AAC“
}

// FindFFprobe looks for ffprobe: explicit path, next to configured ffmpeg,
// PATH, SynoCommunity-Pakete.
func FindFFprobe(ffmpeg string) string {
	var cands []string
	if ffmpeg != "" {
		if strings.HasSuffix(ffmpeg, "ffprobe") {
			cands = append(cands, ffmpeg)
		} else {
			cands = append(cands, filepath.Join(filepath.Dir(ffmpeg), "ffprobe"))
		}
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		cands = append(cands, p)
	}
	for _, pk := range []string{"ffmpeg7", "ffmpeg6", "ffmpeg5", "ffmpeg"} {
		cands = append(cands, "/var/packages/"+pk+"/target/bin/ffprobe", "/var/packages/"+pk+"/target/bin/"+strings.Replace(pk, "ffmpeg", "ffprobe", 1))
	}
	cands = append(cands, "/usr/local/bin/ffprobe7", "/usr/local/bin/ffprobe", "/var/packages/CodecPack/target/bin/ffprobe", "/usr/bin/ffprobe", "/bin/ffprobe")
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return c
		}
	}
	return ""
}

// RunProbe executes ffprobe (Zeitlimit 20 s). Ist tool ein ffmpeg statt
// ffprobe, wird dessen Stream-Übersicht ausgewertet.
func RunProbe(ffprobe, path string) *Probe {
	if IsFFmpegTool(ffprobe) {
		return runFFmpegProbe(ffprobe, path)
	}
	p := &Probe{Container: containerOf(path), Tool: ffprobe}
	if ffprobe == "" {
		return p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		return p
	}
	var r struct {
		Format struct {
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			CodecType    string            `json:"codec_type"`
			CodecName    string            `json:"codec_name"`
			Profile      string            `json:"profile"`
			PixFmt       string            `json:"pix_fmt"`
			Width        int               `json:"width"`
			Height       int               `json:"height"`
			AvgFrameRate string            `json:"avg_frame_rate"`
			BitRate      string            `json:"bit_rate"`
			Channels     int               `json:"channels"`
			Tags         map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &r) != nil {
		return p
	}
	p.Duration, _ = strconv.ParseFloat(r.Format.Duration, 64)
	p.Bitrate, _ = strconv.ParseInt(r.Format.BitRate, 10, 64)
	for _, s := range r.Streams {
		br, _ := strconv.ParseInt(s.BitRate, 10, 64)
		lang, title := s.Tags["language"], s.Tags["title"]
		switch s.CodecType {
		case "video":
			if p.VideoCodec == "" && s.Width > 0 {
				p.VideoBitrate = br
				p.VideoCodec, p.Width, p.Height = s.CodecName, s.Width, s.Height
				p.VideoProfile, p.PixFmt = s.Profile, s.PixFmt
				if a, b, ok := strings.Cut(s.AvgFrameRate, "/"); ok {
					n, _ := strconv.ParseFloat(a, 64)
					d, _ := strconv.ParseFloat(b, 64)
					if d > 0 {
						p.FrameRate = n / d
					}
				}
			}
		case "audio":
			if p.AudioCodec == "" {
				p.AudioCodec, p.Channels, p.AudioBitrate = s.CodecName, s.Channels, br
			}
			p.AudioTracks = append(p.AudioTracks, StreamTrack{Index: len(p.AudioTracks), Codec: s.CodecName, Channels: s.Channels, Language: lang, Title: title, Profile: s.Profile})
		case "subtitle":
			p.Subtitles = append(p.Subtitles, StreamTrack{Index: len(p.Subtitles), Codec: s.CodecName, Language: lang, Title: title})
		}
	}
	p.OK = true
	p.Version = ProbeVersion
	return p
}

func containerOf(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

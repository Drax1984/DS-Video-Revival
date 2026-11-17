package library

// Analyse ohne ffprobe: Synologys eigene FFmpeg-Builds (z.B. CodecPack
// „ffmpeg41“, /bin/ffmpeg) bringen oft kein ffprobe mit. „ffmpeg -i Datei“
// gibt die Stream-Übersicht auf stderr aus; diese wird hier ausgewertet.

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reDuration  = regexp.MustCompile(`Duration: (\d+):(\d\d):(\d\d(?:\.\d+)?)`)
	reTotalRate = regexp.MustCompile(`bitrate: (\d+) kb/s`)
	reStream    = regexp.MustCompile(`^\s*Stream #\d+:\d+(?:\[[^\]]*\])?(?:\(([a-zA-Z]{2,3})\))?[^:]*: (Video|Audio|Subtitle): (.*)$`)
	reSize      = regexp.MustCompile(`, (\d{2,5})x(\d{2,5})`)
	reFPS       = regexp.MustCompile(`([\d.]+) fps`)
	reKbps      = regexp.MustCompile(`(\d+) kb/s`)
	reTitle     = regexp.MustCompile(`^\s+title\s*: (.*)$`)
	reChannels  = regexp.MustCompile(`(\d+) channels`)
	reProfile   = regexp.MustCompile(`^\S+ \(([^)]+)\)`)
	rePixFmt    = regexp.MustCompile(`, (yuv\w+|nv12|p010\w*|gray\w*)[,( ]`)
)

// IsFFmpegTool: true, wenn der Analysepfad ein ffmpeg (kein ffprobe) ist.
func IsFFmpegTool(tool string) bool {
	return tool != "" && !strings.Contains(strings.ToLower(filepath.Base(tool)), "ffprobe")
}

func runFFmpegProbe(ffmpeg, path string) *Probe {
	p := &Probe{Container: containerOf(path), Tool: ffmpeg}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Rückgabewert ist absichtlich ≠ 0 („At least one output file …“); nur stderr zählt.
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-i", path).CombinedOutput()
	return parseFFmpegInfo(string(out), p)
}

func parseFFmpegInfo(out string, p *Probe) *Probe {
	if m := reDuration.FindStringSubmatch(out); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		s, _ := strconv.ParseFloat(m[3], 64)
		p.Duration = float64(h*3600+mi*60) + s
	}
	if m := reTotalRate.FindStringSubmatch(out); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		p.Bitrate = n * 1000
	}
	var last *StreamTrack
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := reStream.FindStringSubmatch(line); m != nil {
			found = true
			last = nil
			lang, kind, rest := m[1], m[2], m[3]
			codec := strings.ToLower(strings.TrimRight(strings.Fields(rest + " ")[0], ","))
			profile := ""
			if m := reProfile.FindStringSubmatch(rest); m != nil && !strings.Contains(m[1], "/") {
				profile = m[1] // „h264 (High 10) (avc1 / …)“ → „High 10“
			}
			var kbps int64
			if k := reKbps.FindStringSubmatch(rest); k != nil {
				kbps, _ = strconv.ParseInt(k[1], 10, 64)
			}
			switch kind {
			case "Video":
				if strings.Contains(rest, "attached pic") || p.VideoCodec != "" {
					continue
				}
				p.VideoCodec, p.VideoBitrate, p.VideoProfile = codec, kbps*1000, profile
				if pf := rePixFmt.FindStringSubmatch(rest); pf != nil {
					p.PixFmt = pf[1]
				}
				if s := reSize.FindStringSubmatch(rest); s != nil {
					p.Width, _ = strconv.Atoi(s[1])
					p.Height, _ = strconv.Atoi(s[2])
				}
				if f := reFPS.FindStringSubmatch(rest); f != nil {
					p.FrameRate, _ = strconv.ParseFloat(f[1], 64)
				}
			case "Audio":
				ch := channelsOf(rest)
				if p.AudioCodec == "" {
					p.AudioCodec, p.Channels, p.AudioBitrate = codec, ch, kbps*1000
				}
				p.AudioTracks = append(p.AudioTracks, StreamTrack{Index: len(p.AudioTracks), Codec: codec, Channels: ch, Language: lang, Profile: profile})
				last = &p.AudioTracks[len(p.AudioTracks)-1]
			case "Subtitle":
				p.Subtitles = append(p.Subtitles, StreamTrack{Index: len(p.Subtitles), Codec: codec, Language: lang})
				last = &p.Subtitles[len(p.Subtitles)-1]
			}
			continue
		}
		if t := reTitle.FindStringSubmatch(line); t != nil && last != nil && last.Title == "" {
			last.Title = strings.TrimSpace(t[1])
		}
	}
	p.OK = found && (p.VideoCodec != "" || p.AudioCodec != "")
	p.Version = ProbeVersion
	return p
}

func channelsOf(s string) int {
	switch {
	case strings.Contains(s, " mono"):
		return 1
	case strings.Contains(s, " stereo"), strings.Contains(s, " downmix"):
		return 2
	case strings.Contains(s, " 7.1"):
		return 8
	case strings.Contains(s, " 6.1"):
		return 7
	case strings.Contains(s, " 5.1"):
		return 6
	case strings.Contains(s, " 5.0"):
		return 5
	case strings.Contains(s, " quad"), strings.Contains(s, " 4.0"):
		return 4
	}
	if m := reChannels.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

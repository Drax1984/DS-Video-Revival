package stream

import (
	"path/filepath"
	"strings"

	"dsvideorevival/internal/library"
)

// Caps describes what a client can play natively.
type Caps struct {
	Name       string
	Containers map[string]bool // mp4, mov, webm, mkv …
	Video      map[string]bool // h264, hevc, vp9, av1 …
	Audio      map[string]bool // aac, mp3, opus, ac3 …
}

func set(v ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range v {
		m[x] = true
	}
	return m
}

// Vordefinierte Client-Profile (konservativ, „gängigste Codecs“).
var (
	// Browser ohne HEVC (Chrome/Firefox/Edge): H.264 + AAC/MP3/Opus in MP4/WebM.
	CapsBrowser = Caps{Name: "browser", Containers: set("mp4", "m4v", "webm"), Video: set("h264", "vp9", "av1"), Audio: set("aac", "mp3", "opus", "vorbis", "flac")}
	// Safari / Apple (AVPlayer): H.264 + HEVC, AAC/MP3/AC3/E-AC3/ALAC in MP4/MOV.
	CapsApple = Caps{Name: "apple", Containers: set("mp4", "m4v", "mov"), Video: set("h264", "hevc"), Audio: set("aac", "mp3", "ac3", "eac3", "alac")}
	// Android (ExoPlayer, Gerätedecoder unbekannt): H.264/HEVC, AAC/MP3/Opus in MP4/MKV/WebM.
	CapsAndroid = Caps{Name: "android", Containers: set("mp4", "m4v", "mkv", "webm"), Video: set("h264", "hevc", "vp9"), Audio: set("aac", "mp3", "opus", "vorbis", "flac")}
)

// Mode of delivery.
const (
	ModeDirect    = "direct"    // Originaldatei (Range)
	ModeRemux     = "remux"     // HLS, Video kopiert, Audio ggf. → AAC
	ModeTranscode = "transcode" // HLS, Video → H.264
)

// Decision is the playback plan.
type Decision struct {
	Mode      string `json:"mode"`
	CopyVideo bool   `json:"copy_video"`
	CopyAudio bool   `json:"copy_audio"`
	HEVC      bool   `json:"hevc"`
	MaxHeight int    `json:"max_height,omitempty"` // 0 = 1080
	// MaxRate: Obergrenze der Videobitrate beim Umwandeln (Bit/s, 0 = Standard),
	// z.B. für langsame Verbindungen von unterwegs.
	MaxRate int64 `json:"max_rate,omitempty"`
	// TS: MPEG-TS-Segmente statt fMP4 (durchsuchbare Liste, nur H.264 –
	// HEVC geht bei Apple nur in fMP4). So liefert auch Jellyfin an iPhone und
	// iPad, wenn die App „ts“ als Container nennt.
	TS bool `json:"ts,omitempty"`
	// Classic: Ablauf wie bis 0.6.6 – für die DS-video-Apps. Die späteren
	// Anpassungen für die Jellyfin-Apps (3-s-Segmente, Drosselung von FFmpeg,
	// Farbumrechnung bei HDR, SDR-Kennzeichnung, schnellere x264-Stufe bei 4K)
	// gelten dort nicht; DS video lief damit einwandfrei.
	Classic bool `json:"classic,omitempty"`
	// Fast4K: 4K-Quelle in Software umwandeln (nur mit Classic): Das Dekodieren
	// von 4K HEVC braucht fast die ganze CPU der NAS. Ohne Entblockungsfilter
	// beim Dekodieren (fällt beim Verkleinern nicht auf), mit schnellem
	// Verkleinern und x264 „ultrafast“ sinkt die übrige Rechenzeit um rund ein
	// Viertel (gemessen), 3-s-Segmente lassen Start und Spulen früher beginnen.
	Fast4K bool   `json:"fast4k,omitempty"`
	Client string `json:"client,omitempty"` // Caps-Name (Wahl des Ton-Encoders)
	// Duration der Quelle (Sekunden). >0: vollständige, durchsuchbare HLS-Liste
	// (VOD) – Springen/Fortsetzen an jede Stelle, FFmpeg startet bei Bedarf neu.
	Duration float64 `json:"duration,omitempty"`
	Reason   string  `json:"reason"`
}

// HLSAudioCopy: Tonformat kann unverändert in fMP4-HLS übernommen werden
// (Apple: AAC, AC-3, E-AC-3 laut HLS-Spezifikation; sonst nur AAC).
func HLSAudioCopy(codec string, c Caps) bool {
	return codec == "aac" || (c.Name == "apple" && (codec == "ac3" || codec == "eac3"))
}

// Decide chooses direct play, remux or transcode.
func Decide(p *library.Probe, path string, c Caps) Decision {
	d := decide(p, path, c)
	d.Client = c.Name
	return d
}

func decide(p *library.Probe, path string, c Caps) Decision {
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if p == nil || !p.OK {
		// Ohne Analyse: nur sicher bekannte Kombination direkt, sonst Transcode.
		if c.Containers[container] && (container == "mp4" || container == "m4v") {
			return Decision{Mode: ModeDirect, Reason: "keine Analyse – MP4 direkt versucht"}
		}
		return Decision{Mode: ModeTranscode, Reason: "keine Analyse möglich"}
	}
	vOK := p.VideoCodec == "" || c.Video[p.VideoCodec] && !H264Exotic(p)
	aOK := p.AudioCodec == "" || c.Audio[p.AudioCodec]
	if c.Containers[container] && vOK && aOK {
		return Decision{Mode: ModeDirect, CopyVideo: true, CopyAudio: true, Reason: "Container/Codecs vom Client unterstützt"}
	}
	copyAudio := p.AudioCodec == "" || HLSAudioCopy(p.AudioCodec, c)
	// HLS (fMP4) kann H.264 und HEVC tragen; VP9/AV1 nur bedingt → umwandeln.
	hlsVideo := (p.VideoCodec == "h264" && !H264Exotic(p)) || (p.VideoCodec == "hevc" && c.Video["hevc"])
	if hlsVideo {
		d := Decision{Mode: ModeRemux, CopyVideo: true, CopyAudio: copyAudio, HEVC: p.VideoCodec == "hevc"}
		d.Reason = "Container " + container + " → HLS"
		if copyAudio && p.AudioCodec != "" {
			d.Reason += ", Ton " + p.AudioCodec + " unverändert"
		} else if p.AudioCodec != "" {
			d.Reason += ", Ton " + p.AudioCodec + " → AAC"
		}
		return d
	}
	if p.VideoCodec == "h264" {
		return Decision{Mode: ModeTranscode, CopyAudio: copyAudio, Reason: "Video H.264 " + exoticLabel(p) + " → H.264 8 Bit"}
	}
	return Decision{Mode: ModeTranscode, CopyAudio: copyAudio, Reason: "Video " + p.VideoCodec + " → H.264"}
}

// H264Exotic: H.264 mit mehr als 8 Bit oder ohne 4:2:0 (z.B. „High 10“,
// „High 4:2:2“). Das können die Hardware-Decoder von iPhone, iPad, Apple TV,
// Android und Browsern nicht – solche Videos werden umgewandelt.
func H264Exotic(p *library.Probe) bool {
	if p == nil || p.VideoCodec != "h264" {
		return false
	}
	return exoticLabel(p) != ""
}

func exoticLabel(p *library.Probe) string {
	prof := strings.ToLower(p.VideoProfile)
	switch {
	case strings.Contains(prof, "4:4:4"):
		return "4:4:4"
	case strings.Contains(prof, "4:2:2"):
		return "4:2:2"
	case strings.Contains(prof, "10"):
		return "10 Bit"
	}
	pf := strings.ToLower(p.PixFmt)
	switch {
	case pf == "" || pf == "yuv420p" || pf == "yuvj420p" || pf == "nv12":
		return ""
	case strings.Contains(pf, "10") || strings.Contains(pf, "12"):
		return "10 Bit"
	}
	return pf
}

// CapsFor maps a DS-video hls_remux device string or browser hint to caps.
func CapsFor(device string) Caps {
	d := strings.ToLower(device)
	switch {
	case strings.Contains(d, "tvos") || strings.Contains(d, "ios") || strings.Contains(d, "iphone") || strings.Contains(d, "ipad") || strings.Contains(d, "apple") || strings.Contains(d, "safari"):
		return CapsApple
	case strings.Contains(d, "android"):
		return CapsAndroid
	}
	return CapsBrowser
}

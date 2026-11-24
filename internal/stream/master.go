package stream

import (
	"fmt"
	"strings"
	"time"
)

// MasterInfo: Angaben zur Quelle für die Übersichtsliste.
type MasterInfo struct {
	Width, Height int
	FPS           float64
	Bitrate       int64    // Bit/s der Quelle (0 = unbekannt)
	AudioCodecs   []string // Ton je Spur (FFmpeg-Name), gewählt wird die Spur des Jobs
	Variant       string   // Adresse der Segmentliste
}

// Master: Übersichtsliste (master.m3u8) für unverändert übernommenes Video
// (HEVC/HDR). Der Apple-Player auf dem iPhone spielt HEVC in fMP4 nur über
// eine Übersicht mit CODECS, RESOLUTION und VIDEO-RANGE – lädt er die
// Segmentliste direkt, fragt er das erste Segment endlos ab (Test mit iOS 18,
// so liefert es auch die Jellyfin-Schnittstelle).
func (m *HLSManager) Master(j *Job, in MasterInfo) string {
	avg := in.Bitrate
	if avg <= 0 {
		avg = 20_000_000
	}
	attrs := []string{fmt.Sprintf("BANDWIDTH=%d", avg*3/2), fmt.Sprintf("AVERAGE-BANDWIDTH=%d", avg)}
	acodec := ""
	if j.audioIndex >= 0 && j.audioIndex < len(in.AudioCodecs) {
		acodec = in.AudioCodecs[j.audioIndex]
	}
	codecs := []string{}
	if v := j.VideoCodecTag(8 * time.Second); v != "" {
		codecs = append(codecs, v)
		if a := m.AudioCodecTag(acodec, j.Decision.CopyAudio, j.Decision.Client); a != "" && acodec != "" {
			codecs = append(codecs, a)
		}
	}
	if len(codecs) > 0 {
		attrs = append(attrs, `CODECS="`+strings.Join(codecs, ",")+`"`)
	}
	if in.Width > 0 && in.Height > 0 {
		attrs = append(attrs, fmt.Sprintf("RESOLUTION=%dx%d", in.Width, in.Height))
	}
	if in.FPS > 0 {
		attrs = append(attrs, fmt.Sprintf("FRAME-RATE=%.3f", in.FPS))
	}
	attrs = append(attrs, "VIDEO-RANGE="+m.VideoRange(j.Src, j.Decision.CopyVideo))
	return "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-STREAM-INF:" + strings.Join(attrs, ",") + "\n" + in.Variant + "\n"
}

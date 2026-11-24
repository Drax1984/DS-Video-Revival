package stream

import (
	"context"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Farbraum beim Umwandeln: H.264 mit 8 Bit ist auf iPhone, Apple TV,
// Android und im Browser immer SDR (BT.709). Ohne Angabe übernimmt FFmpeg die
// HDR-Kennzeichnung der Quelle (PQ/HLG, BT.2020) in das H.264-Video – der
// Apple-Player verweigert so ein Video (lädt das erste Segment endlos), andere
// zeigen blasse Farben. Darum beim Umwandeln von HDR-Quellen auf SDR abbilden
// (zscale + tonemap, falls FFmpeg sie hat), sonst wenigstens die Farbmatrix
// umrechnen; die Ausgabe immer als BT.709 kennzeichnen.

// sdrTags: Kennzeichnung des umgewandelten Videos.
var sdrTags = []string{"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"}

const tonemapChain = "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"

// isHDR: Übertragungsfunktion einer HDR-Quelle.
func isHDR(trc string) bool { return trc == "smpte2084" || trc == "arib-std-b67" }

// Filters: verfügbare Filter von FFmpeg (einmal ermittelt).
func (m *HLSManager) Filters() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.filters != nil || m.FFmpeg == "" {
		return m.filters
	}
	m.filters = map[string]bool{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, m.FFmpeg, "-hide_banner", "-filters").Output()
	if err != nil {
		return m.filters
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 3 && len(f[0]) == 3 {
			m.filters[f[1]] = true
		}
	}
	return m.filters
}

// canTonemap: FFmpeg kann HDR → SDR abbilden.
func (m *HLSManager) canTonemap() bool {
	f := m.Filters()
	return f["zscale"] && f["tonemap"]
}

var (
	reVideoLine = regexp.MustCompile(`(?m)Stream #\d+:\d+.*: Video: .*$`)
	reVideoSize = regexp.MustCompile(`, (\d{2,5})x(\d{2,5})[ ,]`)
	reColorTrio = regexp.MustCompile(`\((?:tv|pc)?,? ?([a-z0-9]+)/([a-z0-9]+)/([a-z0-9-]+)[,)]`)
	reColorOne  = regexp.MustCompile(`\((?:tv|pc), ([a-z0-9-]+)[,)]`)
)

// parseTransfer: Übertragungsfunktion aus der Video-Zeile von „ffmpeg -i“,
// z.B. „yuv420p10le(tv, bt2020nc/bt2020/smpte2084)“ → smpte2084.
func parseTransfer(stderr string) string {
	line := reVideoLine.FindString(stderr)
	if line == "" {
		return ""
	}
	if m := reColorTrio.FindStringSubmatch(line); m != nil {
		return m[3]
	}
	if m := reColorOne.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], "progressive") && !strings.HasPrefix(m[1], "top") && !strings.HasPrefix(m[1], "bottom") {
		return m[1]
	}
	return ""
}

// parseHeight: Bildhöhe aus der Video-Zeile von „ffmpeg -i“.
func parseHeight(stderr string) int {
	if m := reVideoSize.FindStringSubmatch(reVideoLine.FindString(stderr)); m != nil {
		h, _ := strconv.Atoi(m[2])
		return h
	}
	return 0
}

type srcInfo struct {
	trc    string
	height int
}

// srcProbe: Übertragungsfunktion und Höhe der Quelle (je Datei einmal ermittelt).
func (m *HLSManager) srcProbe(path string) srcInfo {
	m.mu.Lock()
	if v, ok := m.srcInfos[path]; ok {
		m.mu.Unlock()
		return v
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, m.FFmpeg, "-hide_banner", "-nostdin", "-i", path).CombinedOutput()
	v := srcInfo{trc: parseTransfer(string(out)), height: parseHeight(string(out))}
	m.mu.Lock()
	if m.srcInfos == nil || len(m.srcInfos) > 500 {
		m.srcInfos = map[string]srcInfo{}
	}
	m.srcInfos[path] = v
	m.mu.Unlock()
	return v
}

func (m *HLSManager) srcTransfer(path string) string { return m.srcProbe(path).trc }
func (m *HLSManager) srcHeight(path string) int      { return m.srcProbe(path).height }

// withPreset ersetzt die Voreinstellung von libx264 (-preset).
func withPreset(enc []string, preset string) []string {
	out := append([]string(nil), enc...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "-preset" {
			out[i+1] = preset
		}
	}
	return out
}

// softwareVF: Filter für die Software-Umwandlung (Größe, ggf. HDR → SDR).
// Bei 4K-Quellen braucht schon das Dekodieren fast alles, was die CPU einer
// NAS schafft – jede Farbumrechnung (gemessen +15 % Rechenzeit mit
// Tonemapping, +35 % nur mit Farbmatrix) kann die Umwandlung dort unter
// Echtzeit drücken. Darüber daher nur verkleinern (blassere Farben), das
// Video nur als SDR gekennzeichnet. (Die DS-video-Apps nutzen diese Filter
// nicht, siehe Decision.Classic.)
func (m *HLSManager) softwareVF(path, maxh string) string {
	sc := "scale=-2:'min(ih," + maxh + ")'"
	if trc := m.srcTransfer(path); isHDR(trc) {
		switch {
		case m.srcHeight(path) > 1440:
			log.Printf("hls: HDR-Quelle (%s) über 1440p → SDR ohne Farbumrechnung (Software-Umwandlung sonst zu langsam)", trc)
			return sc
		case m.canTonemap():
			log.Printf("hls: HDR-Quelle (%s) → SDR mit Tonemapping", trc)
			return sc + "," + tonemapChain
		default:
			log.Printf("hls: HDR-Quelle (%s), FFmpeg ohne zscale/tonemap – Farben werden blass", trc)
		}
	}
	return sc + ":out_color_matrix=bt709"
}

package stream

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"
)

// Angaben für die Übersichtsliste (master.m3u8). Der Apple-Player auf dem
// iPhone braucht sie bei fMP4: Ohne Übersicht lädt er das erste Segment
// endlos, ohne CODECS/RESOLUTION/VIDEO-RANGE meldet er bei HEVC/HDR „Media
// failed to decode“ oder hängt (Test mit iOS 18, Safari und Web-Fenster).
// Die Kennungen werden aus der tatsächlichen Ausgabe gelesen (init.mp4 bzw.
// SPS im ersten MPEG-TS-Segment), nicht geschätzt.

// VideoCodecTag wartet höchstens wait auf die erste Ausgabe des laufenden
// FFmpeg-Laufs und liefert die RFC-6381-Kennung des Videos (z.B.
// „hvc1.2.4.L150.90“, „avc1.640029“) – "" wenn nicht ermittelbar.
func (j *Job) VideoCodecTag(wait time.Duration) string {
	if !j.VOD {
		return ""
	}
	deadline := time.Now().Add(wait)
	for {
		j.mu.Lock()
		g, start := j.gen, j.genStart[j.gen]
		j.mu.Unlock()
		if j.vodTS() {
			if b, err := os.ReadFile(j.genFile(g, j.segName(start))); err == nil && len(b) > 0 {
				return codecFromTS(b)
			}
		} else if b, err := os.ReadFile(j.genFile(g, "init.mp4")); err == nil && len(b) > 0 {
			if c := codecFromInit(b); c != "" {
				return c
			}
		}
		if j.finished() || time.Now().After(deadline) {
			return ""
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// AudioCodecTag: RFC-6381-Kennung des Tons im Stream. src = Codec der Quelle
// (FFmpeg-Name), copied = unverändert übernommen.
func (m *HLSManager) AudioCodecTag(src string, copied bool, client string) string {
	c := strings.ToLower(src)
	if !copied {
		c = "aac"
		if m.aacEncoder() == "" {
			if a := m.audioArgs(client); len(a) > 1 {
				c = a[1]
			}
		}
	}
	switch c {
	case "aac", "libfdk_aac":
		return "mp4a.40.2"
	case "ac3":
		return "ac-3"
	case "eac3":
		return "ec-3"
	case "mp3":
		return "mp4a.40.34"
	case "alac":
		return "alac"
	case "flac":
		return "fLaC"
	case "opus", "libopus":
		return "Opus"
	}
	return ""
}

// VideoRange: HDR-Art des Videos im Stream für VIDEO-RANGE (SDR, PQ, HLG).
// Umgewandeltes Video ist immer SDR (Tonemapping).
func (m *HLSManager) VideoRange(src string, copyVideo bool) string {
	if !copyVideo {
		return "SDR"
	}
	switch m.srcTransfer(src) {
	case "smpte2084":
		return "PQ"
	case "arib-std-b67":
		return "HLG"
	}
	return "SDR"
}

// codecFromInit liest die Kennung aus dem Video-Eintrag (hvcC/avcC) einer
// init.mp4.
func codecFromInit(b []byte) string {
	moov, ok := topBox(b, "moov")
	if !ok {
		return ""
	}
	kids, err := readBoxes(b, moov.start+moov.hdr, moov.end)
	if err != nil {
		return ""
	}
	for _, k := range kids {
		if k.typ != "trak" {
			continue
		}
		stsd, ok := boxPath(b, k, "mdia", "minf", "stbl", "stsd")
		if !ok || stsd.start+stsd.hdr+8 > stsd.end {
			continue
		}
		ents, err := readBoxes(b, stsd.start+stsd.hdr+8, stsd.end)
		if err != nil {
			continue
		}
		for _, e := range ents {
			se := mp4Box{typ: e.typ, start: e.start, hdr: e.hdr + 78, end: e.end} // VisualSampleEntry
			if se.start+se.hdr > se.end {
				continue
			}
			switch e.typ {
			case "hvc1", "hev1":
				if c, ok := childBox(b, se, "hvcC"); ok {
					if s := hevcTag(e.typ, b[c.start+c.hdr:c.end]); s != "" {
						return s
					}
				}
			case "avc1", "avc3":
				if c, ok := childBox(b, se, "avcC"); ok && c.end-(c.start+c.hdr) >= 4 {
					p := b[c.start+c.hdr:]
					return fmt.Sprintf("%s.%02X%02X%02X", e.typ, p[1], p[2], p[3])
				}
			}
		}
	}
	return ""
}

// hevcTag: „hvc1.<Profilraum><Profil>.<Kompatibilität>.<L|H><Level>.<Bytes>“
// (ISO/IEC 14496-15 Anhang E) aus dem hvcC-Inhalt.
func hevcTag(entry string, p []byte) string {
	if len(p) < 13 {
		return ""
	}
	space := []string{"", "A", "B", "C"}[p[1]>>6]
	tier := "L"
	if p[1]&0x20 != 0 {
		tier = "H"
	}
	compat := binary.BigEndian.Uint32(p[2:6])
	var rev uint32
	for i := 0; i < 32; i++ {
		if compat&(1<<uint(i)) != 0 {
			rev |= 1 << uint(31-i)
		}
	}
	cons := p[6:12]
	n := len(cons)
	for n > 0 && cons[n-1] == 0 {
		n--
	}
	s := fmt.Sprintf("%s.%s%d.%X.%s%d", entry, space, p[1]&0x1f, rev, tier, p[12])
	for _, c := range cons[:n] {
		s += fmt.Sprintf(".%X", c)
	}
	return s
}

// codecFromTS sucht im ersten MPEG-TS-Segment die SPS des H.264-Videos.
func codecFromTS(b []byte) string {
	// Video-PID: Paket mit Beginn eines PES für einen Videostrom (0xE0–0xEF)
	pid := -1
	var es []byte
	for o := 0; o+188 <= len(b) && len(es) < 256*1024; o += 188 {
		if b[o] != 0x47 {
			return ""
		}
		pusi := b[o+1]&0x40 != 0
		p := int(binary.BigEndian.Uint16(b[o+1:])) & 0x1fff
		afc := (b[o+3] >> 4) & 3
		h := 4
		if afc&2 != 0 {
			h += 1 + int(b[o+4])
		}
		if afc&1 == 0 || h >= 188 {
			continue
		}
		pl := b[o+h : o+188]
		if pid < 0 {
			if pusi && len(pl) > 9 && pl[0] == 0 && pl[1] == 0 && pl[2] == 1 && pl[3]&0xf0 == 0xe0 {
				pid = p
			} else {
				continue
			}
		}
		if p != pid {
			continue
		}
		if pusi && len(pl) > 9 && pl[0] == 0 && pl[1] == 0 && pl[2] == 1 {
			hl := 9 + int(pl[8]) // PES-Kopf überspringen
			if hl >= len(pl) {
				continue
			}
			pl = pl[hl:]
		}
		es = append(es, pl...)
	}
	for i := 0; i+6 < len(es); i++ {
		if es[i] == 0 && es[i+1] == 0 && es[i+2] == 1 && es[i+3]&0x1f == 7 {
			return fmt.Sprintf("avc1.%02X%02X%02X", es[i+4], es[i+5], es[i+6])
		}
	}
	return ""
}

package stream

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

// Zeitleiste bei FFmpeg-Neustarts (VOD, Sprung an eine Stelle):
// FFmpeg (auch 4.x auf Synology) legt den Startzeitpunkt eines mit -ss
// gestarteten Laufs nicht in die Segmente (tfdt beginnt bei 0), sondern in
// eine leere Edit-Liste der jeweiligen init.mp4. Alle Segmente einer Liste
// teilen sich aber eine init.mp4. Deshalb:
//   - initEdits liest pro Spur die leere Edit-Dauer eines Laufs,
//   - normalizeInit entfernt leere Edits aus der ausgelieferten init.mp4,
//   - shiftSegment addiert den Versatz auf tfdt/sidx der Segmente.
// Ergebnis: absolute Zeitstempel wie bei movflags=+frag_discont neuerer
// FFmpeg-Versionen.

var errMP4 = errors.New("fmp4: ungültige Box")

type mp4Box struct {
	typ   string
	start int // Beginn der Box
	hdr   int // Kopflänge
	end   int // Ende der Box
}

func readBoxes(b []byte, off, end int) ([]mp4Box, error) {
	var out []mp4Box
	for off+8 <= end {
		sz := int(binary.BigEndian.Uint32(b[off:]))
		typ := string(b[off+4 : off+8])
		h := 8
		switch sz {
		case 1:
			if off+16 > end {
				return nil, errMP4
			}
			sz = int(binary.BigEndian.Uint64(b[off+8:]))
			h = 16
		case 0:
			sz = end - off
		}
		if sz < h || off+sz > end {
			return nil, errMP4
		}
		out = append(out, mp4Box{typ, off, h, off + sz})
		off += sz
	}
	return out, nil
}

func childBox(b []byte, parent mp4Box, typ string) (mp4Box, bool) {
	kids, err := readBoxes(b, parent.start+parent.hdr, parent.end)
	if err != nil {
		return mp4Box{}, false
	}
	for _, k := range kids {
		if k.typ == typ {
			return k, true
		}
	}
	return mp4Box{}, false
}

func boxPath(b []byte, parent mp4Box, path ...string) (mp4Box, bool) {
	cur := parent
	for _, p := range path {
		var ok bool
		if cur, ok = childBox(b, cur, p); !ok {
			return cur, false
		}
	}
	return cur, true
}

func topBox(b []byte, typ string) (mp4Box, bool) {
	return childBox(b, mp4Box{start: 0, hdr: 0, end: len(b)}, typ)
}

// mdhdTimescale / mvhdTimescale: Zeitbasis aus (m)vhd/mdhd (Version 0 oder 1).
func hdTimescale(b []byte, bx mp4Box) (uint32, bool) {
	p := bx.start + bx.hdr
	if p >= bx.end {
		return 0, false
	}
	o := p + 4 + 8 // Version/Flags + creation/modification (v0)
	if b[p] == 1 {
		o = p + 4 + 16
	}
	if o+4 > bx.end {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[o:]), true
}

func tkhdID(b []byte, bx mp4Box) (uint32, bool) {
	p := bx.start + bx.hdr
	o := p + 4 + 8
	if b[p] == 1 {
		o = p + 4 + 16
	}
	if o+4 > bx.end {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[o:]), true
}

type elstEntry struct {
	dur   uint64
	media int64
}

func readElst(b []byte, bx mp4Box) ([]elstEntry, byte, bool) {
	p := bx.start + bx.hdr
	if p+8 > bx.end {
		return nil, 0, false
	}
	v := b[p]
	n := int(binary.BigEndian.Uint32(b[p+4:]))
	es := 12
	if v == 1 {
		es = 20
	}
	if p+8+n*es > bx.end {
		return nil, 0, false
	}
	out := make([]elstEntry, n)
	for i := 0; i < n; i++ {
		o := p + 8 + i*es
		if v == 1 {
			out[i] = elstEntry{binary.BigEndian.Uint64(b[o:]), int64(binary.BigEndian.Uint64(b[o+8:]))}
		} else {
			out[i] = elstEntry{uint64(binary.BigEndian.Uint32(b[o:])), int64(int32(binary.BigEndian.Uint32(b[o+4:])))}
		}
	}
	return out, v, true
}

type trackEdit struct {
	ts    uint32 // Zeitbasis der Spur
	empty uint64 // leere Edit-Dauer in Ticks der Spur
	video bool
}

// initEdits: pro Spur-ID die leere Edit-Dauer (Startversatz) eines Laufs.
func initEdits(b []byte) (map[uint32]trackEdit, error) {
	moov, ok := topBox(b, "moov")
	if !ok {
		return nil, errMP4
	}
	mvhd, ok := childBox(b, moov, "mvhd")
	if !ok {
		return nil, errMP4
	}
	movieTS, ok := hdTimescale(b, mvhd)
	if !ok || movieTS == 0 {
		return nil, errMP4
	}
	kids, err := readBoxes(b, moov.start+moov.hdr, moov.end)
	if err != nil {
		return nil, err
	}
	out := map[uint32]trackEdit{}
	for _, k := range kids {
		if k.typ != "trak" {
			continue
		}
		tkhd, ok1 := childBox(b, k, "tkhd")
		mdhd, ok2 := boxPath(b, k, "mdia", "mdhd")
		if !ok1 || !ok2 {
			return nil, errMP4
		}
		id, _ := tkhdID(b, tkhd)
		ts, _ := hdTimescale(b, mdhd)
		if ts == 0 {
			return nil, errMP4
		}
		te := trackEdit{ts: ts}
		if h, ok := boxPath(b, k, "mdia", "hdlr"); ok && h.start+h.hdr+12 <= h.end {
			te.video = string(b[h.start+h.hdr+8:h.start+h.hdr+12]) == "vide"
		}
		var empty uint64
		if elst, ok := boxPath(b, k, "edts", "elst"); ok {
			ents, _, ok := readElst(b, elst)
			if !ok {
				return nil, errMP4
			}
			for _, e := range ents {
				if e.media == -1 {
					empty += e.dur
				}
			}
		}
		// Rundung auf ganze Ticks der Spur (Film-Zeitbasis meist 1/1000 s).
		te.empty = (empty*uint64(ts) + uint64(movieTS)/2) / uint64(movieTS)
		out[id] = te
	}
	return out, nil
}

// runOffsetsFor: Versatz (Ticks) pro Spur, um die Segmente eines Laufs auf
// die gemeinsame Zeitleiste zu legen.
//   - absolute (-copyts, Umverpacken): Zeitstempel sind Originalzeit, der
//     Versatz steht nur in der leeren Edit-Dauer.
//   - sonst (Umwandeln, -ss ohne -copyts): das neu kodierte Video beginnt
//     exakt bei start; kopierter Ton kann davor liegen. Zeit 0 des Laufs ist
//     der früheste Spurbeginn, das Video beginnt bei seiner Edit-Dauer.
func runOffsetsFor(ed map[uint32]trackEdit, start float64, absolute bool) map[uint32]int64 {
	out := map[uint32]int64{}
	base := 0.0 // Sekunden
	if !absolute {
		for _, te := range ed {
			if te.video {
				base = start - float64(te.empty)/float64(te.ts)
				break
			}
		}
	}
	for id, te := range ed {
		out[id] = int64(te.empty) + int64(math.Round(base*float64(te.ts)))
	}
	return out
}

// normalizeInit: Kopie der init.mp4 ohne leere Edits (Größen der
// übergeordneten Boxen werden angepasst).
func normalizeInit(b []byte) ([]byte, error) {
	moov, ok := topBox(b, "moov")
	if !ok {
		return nil, errMP4
	}
	type cut struct{ at, n int } // n Bytes ab at entfernen
	var cuts []cut
	type fix struct {
		pos, hdr int
		delta    int
	}
	var fixes []fix
	kids, err := readBoxes(b, moov.start+moov.hdr, moov.end)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, k := range kids {
		if k.typ != "trak" {
			continue
		}
		edts, ok := childBox(b, k, "edts")
		if !ok {
			continue
		}
		elst, ok := childBox(b, edts, "elst")
		if !ok {
			continue
		}
		ents, v, ok := readElst(b, elst)
		if !ok {
			return nil, errMP4
		}
		es := 12
		if v == 1 {
			es = 20
		}
		removed := 0
		for i, e := range ents {
			if e.media == -1 {
				cuts = append(cuts, cut{elst.start + elst.hdr + 8 + i*es, es})
				removed += es
			}
		}
		if removed == 0 {
			continue
		}
		if removed == len(ents)*es {
			// nur leere Edits → ganze edts-Box entfernen
			removed = edts.end - edts.start
			cuts = cuts[:len(cuts)-countEmpty(ents)]
			cuts = append(cuts, cut{edts.start, removed})
			fixes = append(fixes, fix{k.start, k.hdr, removed})
		} else {
			fixes = append(fixes, fix{elst.start, elst.hdr, removed}, fix{edts.start, edts.hdr, removed}, fix{k.start, k.hdr, removed})
			// Anzahl der Einträge anpassen
			fixes = append(fixes, fix{-(elst.start + elst.hdr + 4), 0, len(ents) - removed/es})
		}
		total += removed
	}
	if total == 0 {
		return append([]byte(nil), b...), nil
	}
	fixes = append(fixes, fix{moov.start, moov.hdr, total})
	out := append([]byte(nil), b...)
	for _, f := range fixes {
		if f.pos < 0 { // Eintragszahl setzen
			binary.BigEndian.PutUint32(out[-f.pos:], uint32(f.delta))
			continue
		}
		if f.hdr == 16 {
			binary.BigEndian.PutUint64(out[f.pos+8:], binary.BigEndian.Uint64(out[f.pos+8:])-uint64(f.delta))
		} else {
			binary.BigEndian.PutUint32(out[f.pos:], binary.BigEndian.Uint32(out[f.pos:])-uint32(f.delta))
		}
	}
	// Schnitte von hinten nach vorn
	sort.Slice(cuts, func(a, b int) bool { return cuts[a].at < cuts[b].at })
	for i := len(cuts) - 1; i >= 0; i-- {
		c := cuts[i]
		out = append(out[:c.at], out[c.at+c.n:]...)
	}
	return out, nil
}

func countEmpty(ents []elstEntry) int {
	n := 0
	for _, e := range ents {
		if e.media == -1 {
			n++
		}
	}
	return n
}

// shiftSegment addiert off (pro Spur) auf tfdt und sidx eines Segments. Die
// Länge bleibt gleich, daher genügt ein Überschreiben an Ort und Stelle.
func shiftSegment(seg []byte, off map[uint32]int64) error {
	top, err := readBoxes(seg, 0, len(seg))
	if err != nil {
		return err
	}
	add := func(pos int, v1 bool, d int64) error {
		var x int64
		if v1 {
			x = int64(binary.BigEndian.Uint64(seg[pos:])) + d
		} else {
			x = int64(binary.BigEndian.Uint32(seg[pos:])) + d
		}
		if x < 0 {
			x = 0 // Ton minimal vor dem ersten Bild: an den Anfang legen
		}
		if v1 {
			binary.BigEndian.PutUint64(seg[pos:], uint64(x))
			return nil
		}
		if x > 0xFFFFFFFF {
			return errors.New("fmp4: Zeitstempel zu groß")
		}
		binary.BigEndian.PutUint32(seg[pos:], uint32(x))
		return nil
	}
	for _, bx := range top {
		switch bx.typ {
		case "sidx":
			p := bx.start + bx.hdr
			if p+20 > bx.end {
				return errMP4
			}
			id := binary.BigEndian.Uint32(seg[p+4:])
			if d := off[id]; d != 0 {
				if err := add(p+12, seg[p] == 1, d); err != nil {
					return err
				}
			}
		case "moof":
			kids, err := readBoxes(seg, bx.start+bx.hdr, bx.end)
			if err != nil {
				return err
			}
			for _, tr := range kids {
				if tr.typ != "traf" {
					continue
				}
				tfhd, ok1 := childBox(seg, tr, "tfhd")
				tfdt, ok2 := childBox(seg, tr, "tfdt")
				if !ok1 || !ok2 || tfhd.start+tfhd.hdr+8 > tfhd.end {
					return errMP4
				}
				id := binary.BigEndian.Uint32(seg[tfhd.start+tfhd.hdr+4:])
				p := tfdt.start + tfdt.hdr
				if d := off[id]; d != 0 {
					if err := add(p+4, seg[p] == 1, d); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// cleanInit entfernt aus der init.mp4, was Apple-Geräte (AVPlayer) an der
// Wiedergabe hindert:
//   - leere sdtp-Boxen (FFmpeg schreibt sie bei Videos mit B-Bildern in die
//     init.mp4 ohne Samples; AVPlayer verweigert dann HEVC),
//   - die Dolby-Vision-Angabe (dvcC/dvvC) bei Profil 7 (UHD-Blu-ray mit
//     Erweiterungsschicht) – das können Apple-Geräte nicht; ohne die Angabe
//     spielen sie die HDR10-Basis ab.
// Bei Fehlern bleibt die Datei unverändert.
func cleanInit(b []byte) []byte {
	moov, ok := topBox(b, "moov")
	if !ok || moov.hdr != 8 {
		return b
	}
	drop := func(typ string, payload []byte) bool {
		switch typ {
		case "sdtp":
			return true // in der init.mp4 gibt es keine Samples
		case "dvcC", "dvvC", "dvwC":
			return len(payload) >= 3 && payload[2]>>1 == 7
		}
		return false
	}
	nm, changed := rebuildBox(b, moov, drop)
	if !changed {
		return b
	}
	out := append([]byte(nil), b[:moov.start]...)
	out = append(out, nm...)
	return append(out, b[moov.end:]...)
}

// containerHead: Länge des Kopfs vor den Kind-Boxen (0 = keine Container-Box).
func containerHead(b []byte, bx mp4Box) int {
	switch bx.typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "mvex":
		return bx.hdr
	case "stsd":
		return bx.hdr + 8 // Version/Flags + Anzahl
	case "hvc1", "hev1", "dvh1", "dvhe", "avc1", "avc3":
		return bx.hdr + 78 // VisualSampleEntry
	}
	return 0
}

func rebuildBox(b []byte, bx mp4Box, drop func(string, []byte) bool) ([]byte, bool) {
	h := containerHead(b, bx)
	if h == 0 || bx.start+h > bx.end {
		return b[bx.start:bx.end], false
	}
	kids, err := readBoxes(b, bx.start+h, bx.end)
	if err != nil {
		return b[bx.start:bx.end], false
	}
	changed := false
	var body []byte
	for _, k := range kids {
		if drop(k.typ, b[k.start+k.hdr:k.end]) {
			changed = true
			continue
		}
		kb, c := rebuildBox(b, k, drop)
		changed = changed || c
		body = append(body, kb...)
	}
	if !changed {
		return b[bx.start:bx.end], false
	}
	out := append([]byte(nil), b[bx.start:bx.start+h]...)
	out = append(out, body...)
	if bx.hdr == 16 {
		binary.BigEndian.PutUint64(out[8:], uint64(len(out)))
	} else {
		binary.BigEndian.PutUint32(out[0:], uint32(len(out)))
	}
	return out, true
}

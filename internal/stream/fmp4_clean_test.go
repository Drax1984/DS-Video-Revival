package stream

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:], typ)
	return append(out, body...)
}

// typesIn: alle Box-Typen unterhalb von moov (Größen müssen stimmen).
func typesIn(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	var walk func(bx mp4Box)
	walk = func(bx mp4Box) {
		out = append(out, bx.typ)
		h := containerHead(b, bx)
		if h == 0 {
			return
		}
		kids, err := readBoxes(b, bx.start+h, bx.end)
		if err != nil {
			t.Fatalf("%s: %v", bx.typ, err)
		}
		for _, k := range kids {
			walk(k)
		}
	}
	moov, ok := topBox(b, "moov")
	if !ok {
		t.Fatal("moov fehlt")
	}
	walk(moov)
	return out
}

func TestCleanInit(t *testing.T) {
	mk := func(dvProfile byte) []byte {
		dv := box("dvcC", []byte{1, 0, dvProfile<<1 | 0, 0x10 | 0x07}, make([]byte, 20))
		entry := box("hvc1", make([]byte, 78), box("hvcC", make([]byte, 30)), dv)
		stsd := box("stsd", []byte{0, 0, 0, 0, 0, 0, 0, 1}, entry)
		stbl := box("stbl", stsd, box("stts", make([]byte, 8)), box("sdtp", []byte{0, 0, 0, 0}))
		moov := box("moov", box("mvhd", make([]byte, 100)), box("trak", box("mdia", box("minf", stbl))), box("mvex", box("trex", make([]byte, 24))))
		return append(box("ftyp", []byte("iso5\x00\x00\x02\x00")), moov...)
	}
	got := typesIn(t, cleanInit(mk(7)))
	want := "moov mvhd trak mdia minf stbl stsd hvc1 hvcC stts mvex trex"
	if s := join(got); s != want {
		t.Fatalf("Profil 7:\n %s\nerwartet\n %s", s, want)
	}
	// Profil 8 (HDR10-kompatibel) behält die Dolby-Vision-Angabe
	if s := join(typesIn(t, cleanInit(mk(8)))); s != "moov mvhd trak mdia minf stbl stsd hvc1 hvcC dvcC stts mvex trex" {
		t.Fatalf("Profil 8: %s", s)
	}
	// nichts zu tun → unverändert
	b := cleanInit(mk(8))
	if !bytes.Equal(cleanInit(b), b) {
		t.Fatal("zweiter Durchlauf verändert die Datei")
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

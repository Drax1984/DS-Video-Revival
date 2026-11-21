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

func TestWithRate(t *testing.T) {
	enc := []string{"-c:v", "libx264", "-crf", "22", "-maxrate", "8M", "-bufsize", "16M", "-g", "48"}
	got := join(withRate(enc, 3_000_000))
	if got != "-c:v libx264 -crf 22 -maxrate 3000000 -bufsize 6000000 -g 48" {
		t.Fatalf("libx264: %s", got)
	}
	if got := join(withRate([]string{"-c:v", "libopenh264", "-b:v", "6M"}, 2_000_000)); got != "-c:v libopenh264 -b:v 2000000" {
		t.Fatalf("openh264: %s", got)
	}
	if got := join(withRate([]string{"-c:v", "x"}, 1_000_000)); got != "-c:v x -maxrate 1000000 -bufsize 2000000" {
		t.Fatalf("ohne Rate: %s", got)
	}
	if got := join(withRate(enc, 0)); got != join(enc) {
		t.Fatalf("0 = unverändert: %s", got)
	}
}

func TestParseTransfer(t *testing.T) {
	for in, want := range map[string]string{
		"  Stream #0:0: Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/smpte2084), 3840x2160, SAR 1:1 DAR 16:9, 23.98 fps": "smpte2084",
		"  Stream #0:0(eng): Video: hevc (Main 10), yuv420p10le(tv, bt2020nc/bt2020/arib-std-b67, progressive), 1920x1080":         "arib-std-b67",
		"  Stream #0:0: Video: h264 (High), yuv420p(tv, bt709, progressive), 1920x800 [SAR 1:1 DAR 12:5], 23.98 fps":               "bt709",
		"  Stream #0:0: Video: h264 (High), yuv420p(progressive), 1280x720, 25 fps":                                                "",
		"  Stream #0:1: Audio: eac3, 48000 Hz, 5.1(side), fltp, 640 kb/s":                                                          "",
	} {
		if got := parseTransfer(in); got != want {
			t.Errorf("%q → %q, erwartet %q", in, got, want)
		}
	}
}

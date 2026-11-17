// genicons erzeugt die Paket- und Menü-Icons (eigenes, generisches Motiv:
// abgerundetes Quadrat mit Wiedergabe-Dreieck). Aufruf: go run ./tools/genicons <zielordner>
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

func icon(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	r := s * 0.22
	top := color.RGBA{0x3a, 0x86, 0xf0, 0xff}
	bot := color.RGBA{0x1b, 0x4f, 0xb8, 0xff}
	for y := 0; y < size; y++ {
		t := float64(y) / s
		c := color.RGBA{lerp(top.R, bot.R, t), lerp(top.G, bot.G, t), lerp(top.B, bot.B, t), 0xff}
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			a := roundedAlpha(fx, fy, s, r)
			if a <= 0 {
				continue
			}
			px := c
			if inTriangle(fx/s, fy/s) {
				px = color.RGBA{0xff, 0xff, 0xff, 0xff}
			}
			px.A = uint8(a * 255)
			px.R, px.G, px.B = uint8(float64(px.R)*a), uint8(float64(px.G)*a), uint8(float64(px.B)*a)
			img.SetRGBA(x, y, px)
		}
	}
	return img
}

func lerp(a, b uint8, t float64) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }

func roundedAlpha(x, y, s, r float64) float64 {
	cx := math.Max(r, math.Min(s-r, x))
	cy := math.Max(r, math.Min(s-r, y))
	d := math.Hypot(x-cx, y-cy)
	return math.Max(0, math.Min(1, r-d+0.5))
}

// Wiedergabe-Dreieck, optisch zentriert.
func inTriangle(x, y float64) bool {
	ax, ay, bx, by, cx, cy := 0.38, 0.28, 0.38, 0.72, 0.74, 0.50
	d1 := (x-bx)*(ay-by) - (ax-bx)*(y-by)
	d2 := (x-cx)*(by-cy) - (bx-cx)*(y-cy)
	d3 := (x-ax)*(cy-ay) - (cx-ax)*(y-ay)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

func write(path string, size int) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, icon(size)); err != nil {
		panic(err)
	}
}

func main() {
	out := os.Args[1]
	_ = os.MkdirAll(filepath.Join(out, "ui", "images"), 0o755)
	write(filepath.Join(out, "PACKAGE_ICON.PNG"), 64)
	write(filepath.Join(out, "PACKAGE_ICON_256.PNG"), 256)
	for _, s := range []int{16, 24, 32, 48, 64, 72, 256} {
		write(filepath.Join(out, "ui", "images", "icon_"+strconv.Itoa(s)+".png"), s)
	}
}

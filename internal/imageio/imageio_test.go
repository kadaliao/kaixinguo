package imageio

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"
	"testing"
)

// decodes reports whether Decode reads pictures on this system.
var decodes = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// A picture comes out scaled to the width asked for, its colors in the
// right channels.
func TestDecodeScales(t *testing.T) {
	if !decodes {
		t.Skip("no decoder on " + runtime.GOOS)
	}
	src := image.NewRGBA(image.Rect(0, 0, 800, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 800; x++ {
			c := color.RGBA{230, 20, 20, 255} // red on the left
			if x >= 400 {
				c = color.RGBA{20, 20, 230, 255} // blue on the right
			}
			src.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	img, err := Decode(buf.Bytes(), 400)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 400 || b.Dy() != 100 {
		t.Fatalf("size %v", b)
	}
	if c := img.RGBAAt(50, 50); c.R < 180 || c.B > 80 || c.A != 255 {
		t.Fatalf("left is %v, not red", c)
	}
	if c := img.RGBAAt(350, 50); c.B < 180 || c.R > 80 || c.A != 255 {
		t.Fatalf("right is %v, not blue", c)
	}
}

// TestHEIC decodes the HEIC file IMAGEIO_SAMPLE names, on macOS and
// Windows.
func TestHEIC(t *testing.T) {
	path := os.Getenv("IMAGEIO_SAMPLE")
	if path == "" || !decodes {
		t.Skip("IMAGEIO_SAMPLE is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := Decode(data, 200)
	if err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("IMAGEIO_OUT"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}
	b := img.Bounds()
	if b.Dx() != 200 || b.Dy() < 100 {
		t.Fatalf("size %v", b)
	}
	// A cover is not one flat color.
	first := img.RGBAAt(0, 0)
	same := true
	for y := 0; y < b.Dy() && same; y += 7 {
		for x := 0; x < b.Dx(); x += 7 {
			if img.RGBAAt(x, y) != first {
				same = false
				break
			}
		}
	}
	if same {
		t.Fatal("decoded a flat picture")
	}
}

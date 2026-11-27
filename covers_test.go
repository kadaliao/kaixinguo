package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A cover downloads once: it comes scaled down to coverWidth and kept on
// disk as JPEG, which later loads read, as after it left memory.
func TestCoverKeptOnDisk(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("covers are not scaled or kept on " + runtime.GOOS)
	}
	poster := image.NewRGBA(image.Rect(0, 0, 800, 1066))
	for y := 0; y < 1066; y++ {
		for x := 0; x < 800; x++ {
			poster.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, poster); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(data.Bytes())
	}))
	defer srv.Close()

	dir := t.TempDir()
	cs := newCovers(dir, func(fn func()) { fn() })
	raw := srv.URL + "/poster.heic?sig=1"
	k := coverKey(raw)
	bmp, fresh, err := cs.bitmap(k, raw)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := bmp.Size(); !fresh || w != coverWidth || h != 533 {
		t.Fatalf("a %d × %d cover, fresh %v", w, h, fresh)
	}
	f, err := os.Open(filepath.Join(dir, k+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if cfg, err := jpeg.DecodeConfig(f); err != nil || cfg.Width != coverWidth {
		t.Fatalf("kept on disk as %+v, %v", cfg, err)
	}

	// The next signature names the same cover.
	if _, fresh, err := cs.bitmap(coverKey(srv.URL+"/poster.heic?sig=2"), raw); err != nil || fresh {
		t.Fatalf("read again: fresh %v, %v", fresh, err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("downloaded %d times", n)
	}
}

// A cover that fails to load, as a HEIC one does where the system has no
// decoder for it, stays empty and waits before it is tried again, rather
// than downloading on every frame.
func TestCoverFailureWaits(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	// The test's goroutine is the main thread: it runs what loads hand back.
	results := make(chan func(), 4)
	cs := newCovers(t.TempDir(), func(fn func()) { results <- fn })
	raw := srv.URL + "/poster.heic"
	var shown *ui.Bitmap
	tt := ui.NewTester(func(c *ui.Context) {
		shown, _ = cs.get(c, raw)
		ui.Text(c, "封面")
	}, 200, 200)
	(<-results)()
	for range 5 {
		tt.Frame()
	}
	if cs.pending[coverKey(raw)] || hits.Load() != 1 || shown != nil {
		t.Fatalf("loads again at once: downloaded %d times, showing %v", hits.Load(), shown)
	}

	// Once the wait is over, the next frame tries again.
	cs.failed[coverKey(raw)] = time.Now().Add(-coverRetry)
	tt.Frame()
	(<-results)()
	if n := hits.Load(); n != 2 {
		t.Fatalf("downloaded %d times after the wait", n)
	}
}

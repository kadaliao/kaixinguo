package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/kadaliao/kaixinguo/internal/imageio"
)

// coverWidth is the width covers are kept at, in pixels: a grid's poster
// at twice its size on a Retina display.
const coverWidth = 400

// coverIdle is how long a cover no frame shows stays in memory; then only
// its JPEG on disk does, which decodes again in a few milliseconds.
const coverIdle = time.Minute

// coverRetry is how long a cover that failed to load waits before a frame
// tries it again. Without the wait a cover the system cannot decode, as
// HEIC on a Windows without the HEIF extension, downloads on every frame.
const coverRetry = time.Minute

// covers downloads posters once, keeps them on disk as JPEG, and shows
// them as bitmaps. Its maps belong to the main thread.
type covers struct {
	dir    string
	update func(func()) // runs on the main thread
	client *http.Client
	sem    chan struct{}

	loaded  map[string]*cover
	pending map[string]bool
	failed  map[string]time.Time // when a cover last failed to load
	swept   time.Time
}

type cover struct {
	bmp  *ui.Bitmap
	at   time.Time // when it arrived from the network, to fade it in
	used time.Time // when a frame last showed it
}

func newCovers(dir string, update func(func())) *covers {
	_ = os.MkdirAll(dir, 0o755)
	return &covers{
		dir:     dir,
		update:  update,
		client:  &http.Client{Timeout: 30 * time.Second},
		sem:     make(chan struct{}, 6),
		loaded:  map[string]*cover{},
		pending: map[string]bool{},
		failed:  map[string]time.Time{},
	}
}

// key names a cover by its URL without the query, whose signature changes
// from one response to the next.
func coverKey(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		raw = u.Host + u.Path
	}
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// get returns the cover at raw and how opaque to show it as it fades in,
// starting its download when it is not here yet. Call it in the view.
func (cs *covers) get(c *ui.Context, raw string) (*ui.Bitmap, float32) {
	if raw == "" {
		return nil, 0
	}
	now := time.Now()
	if now.Sub(cs.swept) > 10*time.Second {
		cs.sweep(now)
	}
	k := coverKey(raw)
	if cv := cs.loaded[k]; cv != nil {
		cv.used = now
		alpha := float32(time.Since(cv.at)) / float32(200*time.Millisecond)
		if alpha < 1 {
			c.Invalidate()
			return cv.bmp, alpha
		}
		return cv.bmp, 1
	}
	if !cs.pending[k] && now.Sub(cs.failed[k]) >= coverRetry {
		cs.pending[k] = true
		go cs.load(k, raw)
	}
	return nil, 0
}

// sweep lets go of the covers no frame showed for a while, and of the
// failures that no longer hold a cover back.
func (cs *covers) sweep(now time.Time) {
	cs.swept = now
	for k, cv := range cs.loaded {
		if now.Sub(cv.used) > coverIdle {
			delete(cs.loaded, k)
		}
	}
	for k, at := range cs.failed {
		if now.Sub(at) >= coverRetry {
			delete(cs.failed, k)
		}
	}
}

func (cs *covers) load(k, raw string) {
	cs.sem <- struct{}{}
	defer func() { <-cs.sem }()
	bmp, fresh, err := cs.bitmap(k, raw)
	if err != nil && os.Getenv("KX_DEBUG") != "" {
		log.Printf("cover: %v", err)
	}
	cs.update(func() {
		delete(cs.pending, k)
		if err != nil {
			cs.failed[k] = time.Now()
			return
		}
		cv := &cover{bmp: bmp, used: time.Now()}
		if fresh {
			cv.at = time.Now() // fades in; those from the disk show at once
		}
		cs.loaded[k] = cv
	})
}

// bitmap reads the cover from the disk, else downloads it; fresh tells
// which.
func (cs *covers) bitmap(k, raw string) (bmp *ui.Bitmap, fresh bool, err error) {
	path := filepath.Join(cs.dir, k+".jpg")
	if data, err := os.ReadFile(path); err == nil {
		if bmp, err := ui.DecodeBitmap(data); err == nil {
			return bmp, false, nil
		}
	}
	resp, err := cs.client.Get(raw)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, true, errors.New(resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		bmp, err := ui.DecodeBitmap(data) // no HEIC here
		return bmp, true, err
	}
	// ImageIO on macOS and WIC on Windows read HEIC, which the CDN serves,
	// and scale it down.
	img, err := imageio.Decode(data, coverWidth)
	if err != nil {
		return nil, true, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, true, err
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
	bmp, err = ui.DecodeBitmap(buf.Bytes())
	return bmp, true, err
}

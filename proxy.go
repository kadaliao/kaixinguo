package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kadaliao/kaixinguo/internal/cenc"
)

// proxy serves the player page and the episodes to the web view, from
// 127.0.0.1. Episodes are decrypted as they stream from the CDN, a range
// at a time, so the player starts at once and seeks anywhere.
type proxy struct {
	host    string // 127.0.0.1:port
	base    string // http://127.0.0.1:port/<secret>
	client  *http.Client
	onEvent func(url.Values)

	mu      sync.Mutex
	streams map[string]*stream
	order   []string
}

// stream is one episode's file on the CDN.
type stream struct {
	urls []string
	key  []byte // nil for a clear file

	mu     sync.Mutex
	ready  bool
	size   int64
	layout *cenc.Layout
	good   int // the URL that answered
}

func newProxy(onEvent func(url.Values)) (*proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &proxy{
		host:    ln.Addr().String(),
		client:  &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 20 * time.Second, MaxIdleConnsPerHost: 8}},
		onEvent: onEvent,
		streams: map[string]*stream{},
	}
	p.base = "http://" + p.host + "/" + randomHex(12)
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return p, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *proxy) playerURL() string { return p.base + "/player" }

// add makes the file at urls (a CDN URL and its backup) playable, and
// returns the URL the web view plays it from.
func (p *proxy) add(urls []string, key []byte) string {
	token := randomHex(8)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streams[token] = &stream{urls: urls, key: key}
	p.order = append(p.order, token)
	// Keep the last few: the one playing, and one the player may still read.
	for len(p.order) > 4 {
		delete(p.streams, p.order[0])
		p.order = p.order[1:]
	}
	return p.base + "/v/" + token + ".mp4"
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != p.host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	prefix := strings.TrimPrefix(p.base, "http://"+p.host)
	rest, ok := strings.CutPrefix(r.URL.Path, prefix+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case rest == "player":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, playerHTML)
	case rest == "event":
		if p.onEvent != nil {
			p.onEvent(r.URL.Query())
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(rest, "v/"):
		token := strings.TrimSuffix(strings.TrimPrefix(rest, "v/"), ".mp4")
		p.mu.Lock()
		s := p.streams[token]
		p.mu.Unlock()
		if s == nil {
			http.NotFound(w, r)
			return
		}
		p.serveStream(w, r, s)
	default:
		http.NotFound(w, r)
	}
}

func (p *proxy) serveStream(w http.ResponseWriter, r *http.Request, s *stream) {
	ctx := r.Context()
	if err := p.prepare(ctx, s); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	start, end, partial, ok := parseRange(r.Header.Get("Range"), s.size)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", s.size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	var body io.ReadCloser
	if r.Method != http.MethodHead {
		var err error
		if body, err = p.open(ctx, s, start, end); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer body.Close()
	}
	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, s.size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if body == nil {
		return
	}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64<<10)
	for pos := start; pos <= end; {
		n, err := body.Read(buf[:min(int64(len(buf)), end-pos+1)])
		if n > 0 {
			chunk := buf[:n]
			if s.layout != nil {
				s.layout.Decrypt(chunk, pos)
			}
			if _, werr := w.Write(chunk); werr != nil {
				return // the player let go of this range
			}
			if flusher != nil {
				flusher.Flush()
			}
			pos += int64(n)
		}
		if err != nil {
			return
		}
	}
}

// prepare learns the file's size and, for an encrypted one, its layout.
func (p *proxy) prepare(ctx context.Context, s *stream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return nil
	}
	var lastErr error
	for i, u := range s.urls {
		head, total, err := p.fetch(ctx, u, 0, 64<<10-1)
		if err != nil {
			lastErr = err
			continue
		}
		s.size, s.good = total, i
		if s.key != nil {
			read := func(off, n int64) ([]byte, error) {
				if off+n <= int64(len(head)) {
					return head[off : off+n], nil
				}
				b, _, err := p.fetch(ctx, u, off, off+n-1)
				return b, err
			}
			l, err := cenc.Open(read, total, s.key)
			if err != nil {
				return err
			}
			s.layout = l
		}
		s.ready = true
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no stream URL")
	}
	return lastErr
}

// fetch reads bytes a to b of u, and the file's size.
func (p *proxy) fetch(ctx context.Context, u string, a, b int64) ([]byte, int64, error) {
	resp, err := p.get(ctx, u, a, b)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	total := resp.ContentLength
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			total, _ = strconv.ParseInt(cr[i+1:], 10, 64)
		}
	}
	if resp.StatusCode == http.StatusOK && a > 0 {
		if _, err := io.CopyN(io.Discard, resp.Body, a); err != nil {
			return nil, 0, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, b-a+1))
	if err != nil {
		return nil, 0, err
	}
	if total <= 0 {
		return nil, 0, errors.New("the CDN did not tell the file's size")
	}
	return data, total, nil
}

// open streams bytes a to b of the stream, from the URL that answered,
// else the other.
func (p *proxy) open(ctx context.Context, s *stream, a, b int64) (io.ReadCloser, error) {
	order := []int{s.good}
	for i := range s.urls {
		if i != s.good {
			order = append(order, i)
		}
	}
	var lastErr error
	for _, i := range order {
		resp, err := p.get(ctx, s.urls[i], a, b)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusOK && a > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, a); err != nil {
				resp.Body.Close()
				lastErr = err
				continue
			}
		}
		return resp.Body, nil
	}
	return nil, lastErr
}

func (p *proxy) get(ctx context.Context, u string, a, b int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", a, b))
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("CDN answered %s", resp.Status)
	}
	return resp, nil
}

// parseRange reads a Range header of one range against a file of size
// bytes; partial is false for no header.
func parseRange(h string, size int64) (start, end int64, partial, ok bool) {
	if h == "" {
		return 0, size - 1, false, size > 0
	}
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, false
	}
	first, last, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, 0, false, false
	}
	if first == "" { // the last n bytes
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, false
		}
		return max(0, size-n), size - 1, true, size > 0
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, false
	}
	end = size - 1
	if last != "" {
		if end, err = strconv.ParseInt(last, 10, 64); err != nil || end < start {
			return 0, 0, false, false
		}
		end = min(end, size-1)
	}
	return start, end, true, true
}

// playerHTML is the page of the web view: a video and the calls the app
// makes to it. It tells the app what the video does through /event.
const playerHTML = `<!doctype html>
<html><head><meta charset="utf-8">
<style>
html,body{margin:0;height:100%;background:#000;overflow:hidden;-webkit-user-select:none;cursor:default}
video{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;background:#000;outline:none}
#hud{position:fixed;top:64px;left:50%;transform:translateX(-50%);padding:7px 14px;border-radius:9px;
  background:rgba(18,18,20,.78);color:#fff;font:500 12px/1 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;
  letter-spacing:.02em;opacity:0;transition:opacity .16s ease;pointer-events:none;z-index:3;white-space:nowrap}
#hud.show{opacity:1}
#bar{position:fixed;left:12px;right:12px;bottom:12px;height:56px;display:flex;align-items:center;gap:8px;
  padding:0 12px;border-radius:15px;background:rgba(24,24,26,.82);-webkit-backdrop-filter:blur(22px);
  backdrop-filter:blur(22px);box-shadow:0 10px 34px rgba(0,0,0,.38);color:#fff;z-index:2;
  opacity:0;transform:translateY(8px);transition:opacity .18s ease,transform .18s ease;pointer-events:none}
body.controls #bar{opacity:1;transform:none;pointer-events:auto}
body:not(.controls){cursor:none}
#bar .ctl{width:28px;height:28px;flex:none;padding:0;border:0;border-radius:8px;background:transparent;
  color:rgba(255,255,255,.9);display:flex;align-items:center;justify-content:center;cursor:pointer}
#bar .ctl:hover{background:rgba(255,255,255,.12)}
#bar .ctl:active{background:rgba(255,255,255,.2)}
#bar .ctl svg{width:18px;height:18px;fill:none;stroke:currentColor;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round}
#bar .time{min-width:32px;flex:none;text-align:center;color:rgba(255,255,255,.72);
  font:500 11px/1 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;font-variant-numeric:tabular-nums}
#play .pause{display:none}
body.playing #play .play{display:none}
body.playing #play .pause{display:block}
body.muted #mute .wave{opacity:.18}
input[type=range]{-webkit-appearance:none;appearance:none;height:4px;border-radius:2px;outline:none;margin:0;
  background:linear-gradient(to right,rgba(255,255,255,.92) var(--p,0%),rgba(255,255,255,.22) var(--p,0%))}
input[type=range]::-webkit-slider-thumb{-webkit-appearance:none;width:11px;height:11px;border-radius:50%;
  background:#fff;box-shadow:0 1px 4px rgba(0,0,0,.4);opacity:0;transition:opacity .12s}
#bar:hover input[type=range]::-webkit-slider-thumb{opacity:1}
#seek{flex:1;min-width:50px}
#vol{width:76px;flex:none}
#rate{width:auto;min-width:46px;padding:0 6px;
  font:600 11px/1 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;font-variant-numeric:tabular-nums}
#rates{position:absolute;right:48px;bottom:calc(100% + 8px);flex-direction:column;gap:2px;
  min-width:72px;padding:6px;border-radius:12px;background:rgba(24,24,26,.92);
  -webkit-backdrop-filter:blur(22px);backdrop-filter:blur(22px);box-shadow:0 10px 34px rgba(0,0,0,.38)}
#rates[hidden]{display:none}
#rates:not([hidden]){display:flex}
#rates button{height:28px;border:0;border-radius:8px;background:transparent;color:rgba(255,255,255,.9);
  font:500 12px/1 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;font-variant-numeric:tabular-nums;cursor:pointer}
#rates button:hover,#rates button.on{background:rgba(255,255,255,.14)}
</style></head>
<body><video id="v" playsinline preload="auto"></video>
<div id="bar" role="group" aria-label="播放控制">
  <button id="play" class="ctl" aria-label="播放">
    <svg class="play" viewBox="0 0 24 24"><path d="M8 5.2v13.6L19 12z" fill="currentColor" stroke="none"/></svg>
    <svg class="pause" viewBox="0 0 24 24"><path d="M7 5h3.4v14H7zM13.6 5H17v14h-3.4z" fill="currentColor" stroke="none"/></svg>
  </button>
  <span id="cur" class="time">0:00</span>
  <input id="seek" type="range" min="0" max="1000" step="1" value="0" aria-label="播放进度">
  <span id="dur" class="time">0:00</span>
  <button id="mute" class="ctl" aria-label="静音">
    <svg viewBox="0 0 24 24"><path d="M4 9h4l5-4v14l-5-4H4z" fill="currentColor" stroke="none"/><path class="wave" d="M16 9.2a4 4 0 0 1 0 5.6M18.6 6.8a7.5 7.5 0 0 1 0 10.4"/></svg>
  </button>
  <input id="vol" type="range" min="0" max="1" step="0.01" value="1" aria-label="音量">
  <button id="rate" class="ctl" aria-label="播放速度 1.0×">1.0×</button>
  <div id="rates" role="menu" aria-label="播放速度" hidden>
    <button type="button" data-rate="0.5">0.5×</button>
    <button type="button" data-rate="0.75">0.75×</button>
    <button type="button" data-rate="1" class="on">1.0×</button>
    <button type="button" data-rate="1.25">1.25×</button>
    <button type="button" data-rate="1.5">1.5×</button>
    <button type="button" data-rate="2">2.0×</button>
  </div>
  <button id="pip" class="ctl" aria-label="小窗播放">
    <svg viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="2.2"/><rect x="12" y="12" width="7.5" height="5.5" rx="1.2" fill="currentColor" stroke="none"/></svg>
  </button>
</div>
<div id="hud"></div>
<script>
const v = document.getElementById('v');
const hud = document.getElementById('hud');
const bar = document.getElementById('bar');
const seek = document.getElementById('seek');
const vol = document.getElementById('vol');
const cur = document.getElementById('cur');
const dur = document.getElementById('dur');
const playBtn = document.getElementById('play');
const muteBtn = document.getElementById('mute');
const pipBtn = document.getElementById('pip');
const rateBtn = document.getElementById('rate');
const rateMenu = document.getElementById('rates');
const speeds = [0.5, 0.75, 1, 1.25, 1.5, 2];
let ep = '', last = 0, brightness = 1, hudTimer = 0, hideTimer = 0, seeking = false;
let rate = 1;
let hold = false, dblInterval = 500;
function send(type, extra) {
  fetch('event?type=' + type + '&ep=' + encodeURIComponent(ep) + (extra || '')).catch(() => {});
}
function clamp(n, lo, hi) { return Math.max(lo, Math.min(hi, n)) }
function fmt(t) {
  if (!isFinite(t) || t < 0) t = 0;
  t = Math.floor(t);
  return Math.floor(t / 60) + ':' + (t % 60 < 10 ? '0' : '') + (t % 60);
}
function fill(el) {
  const lo = +el.min || 0, hi = +el.max || 1;
  el.style.setProperty('--p', ((+el.value - lo) / (hi - lo) * 100) + '%');
}
function syncTime() {
  if (!seeking && isFinite(v.duration) && v.duration > 0) {
    seek.value = Math.round(v.currentTime / v.duration * 1000);
  }
  fill(seek); fill(vol);
  cur.textContent = fmt(v.currentTime);
  dur.textContent = fmt(v.duration);
}
function syncVolume() {
  vol.value = v.muted ? 0 : v.volume;
  fill(vol);
  document.body.classList.toggle('muted', v.muted || v.volume === 0);
}
function syncPlay() {
  document.body.classList.toggle('playing', !v.paused);
  playBtn.setAttribute('aria-label', v.paused ? '播放' : '暂停');
}
function hideControls() {
  rateMenu.hidden = true;
  document.body.classList.remove('controls');
}
function showControls() {
  document.body.classList.add('controls');
  clearTimeout(hideTimer);
  if (!v.paused && rateMenu.hidden) hideTimer = setTimeout(hideControls, 2600);
}
function rateLabel(r) { return r.toFixed(2).replace(/0$/, '') + '\u00d7' }
function applyRate() {
  v.defaultPlaybackRate = rate;
  v.playbackRate = rate;
  rateBtn.textContent = rateLabel(rate);
  rateBtn.setAttribute('aria-label', '播放速度 ' + rateLabel(rate));
  for (const b of rateMenu.children) b.classList.toggle('on', +b.dataset.rate === rate);
}
function setRate(r) {
  if (speeds.indexOf(r) < 0) return;
  rate = r;
  applyRate();
  rateMenu.hidden = true;
  showHud('速度 ' + rateLabel(rate));
  showControls();
}
function showHud(text) {
  hud.textContent = text;
  hud.classList.add('show');
  clearTimeout(hudTimer);
  hudTimer = setTimeout(() => hud.classList.remove('show'), 900);
}
window.kx = {
  play(src, id, start) {
    ep = id; last = 0; seeking = false;
    v.src = src;
    applyRate(); // a new src can clear the element's rate
    if (start > 0) v.addEventListener('loadedmetadata', () => { v.currentTime = start }, {once: true});
    if (!hold) v.play().catch(e => { if (e.name !== 'AbortError') send('error', '&msg=' + encodeURIComponent(e.message)) });
  },
  stop() { v.pause(); v.removeAttribute('src'); v.load(); },
  pause() { v.pause() },
  setHold(on) {
    hold = !!on;
    if (hold) v.pause();
    else if (v.src) v.play().catch(() => {});
  },
  setDoubleClickInterval(ms) {
    if (isFinite(ms) && ms >= 100 && ms <= 5000) dblInterval = ms;
  },
  toggle() { v.paused ? v.play() : v.pause() },
  seek(d) {
    v.currentTime = Math.max(0, Math.min(v.duration || Infinity, v.currentTime + d));
    showHud((d < 0 ? '快退 ' : '快进 ') + Math.abs(d) + ' 秒');
    showControls();
  },
  nudgeRate(d) {
    let i = speeds.indexOf(rate);
    if (i < 0) i = speeds.indexOf(1);
    const n = i + d;
    if (n < 0 || n >= speeds.length) return;
    setRate(speeds[n]);
  },
};
// The right half of the video scrolls the volume, the left half its
// brightness, by a mouse's wheel or a touchpad.
window.addEventListener('wheel', e => {
  const dy = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
  if (!v.src || Math.abs(dy) < 0.5) return;
  e.preventDefault();
  if (e.clientX >= window.innerWidth / 2) {
    const before = v.volume;
    v.volume = clamp(v.volume - dy / 800, 0, 1);
    if (v.volume === 0) v.muted = true;
    else if (v.volume > before) v.muted = false;
    syncVolume();
    showHud('音量 ' + Math.round(v.volume * 100) + '%');
  } else {
    brightness = clamp(brightness - dy / 1000, 0.25, 1.6);
    v.style.filter = 'brightness(' + brightness.toFixed(2) + ')';
    showHud('亮度 ' + Math.round(brightness * 100) + '%');
  }
}, {passive: false});
playBtn.addEventListener('click', () => kx.toggle());
muteBtn.addEventListener('click', () => {
  if (v.volume === 0) { v.volume = 1; v.muted = false } else { v.muted = !v.muted }
  syncVolume();
});
pipBtn.addEventListener('click', () => {
  // The page's own API keeps the click's gesture; Go's toggle is the
  // fallback for a WebKit that doesn't expose it.
  if (document.pictureInPictureElement) {
    document.exitPictureInPicture().catch(() => send('pip'));
  } else if (v.requestPictureInPicture) {
    v.requestPictureInPicture().catch(() => send('pip'));
  } else {
    send('pip');
  }
});
vol.addEventListener('input', () => {
  v.volume = +vol.value;
  v.muted = v.volume === 0;
  syncVolume();
});
// Blur only when the pointer lets go, so the arrow keys keep working
// after a keyboard volume change.
vol.addEventListener('pointerup', () => vol.blur());
// A click on the picture plays or pauses it, as the video's controls do;
// a double click, held back until the system's double-click window has
// passed, changes to or from the theater instead.
let clickTimer = 0;
v.addEventListener('click', e => {
  clearTimeout(clickTimer);
  if (e.detail > 1) return;
  // A little past the system interval, so the dblclick still wins at the
  // boundary.
  clickTimer = setTimeout(() => kx.toggle(), dblInterval + 40);
});
v.addEventListener('dblclick', () => {
  clearTimeout(clickTimer);
  send('theater');
});
seek.addEventListener('input', () => {
  seeking = true;
  if (isFinite(v.duration)) v.currentTime = seek.value / 1000 * v.duration;
  fill(seek); cur.textContent = fmt(v.currentTime);
});
function endSeek(blur) {
  if (seeking) { seeking = false; syncTime() }
  if (blur) seek.blur();
}
seek.addEventListener('pointerup', () => endSeek(true));
seek.addEventListener('change', () => endSeek(false));
document.addEventListener('pointerup', () => { if (seeking) endSeek(true) });
v.addEventListener('loadedmetadata', () => { applyRate(); syncTime() });
v.addEventListener('durationchange', syncTime);
v.addEventListener('timeupdate', syncTime);
v.addEventListener('volumechange', syncVolume);
v.addEventListener('play', () => { syncPlay(); showControls() });
v.addEventListener('pause', () => { syncPlay(); showControls() });
window.addEventListener('mousemove', e => {
  if (e.target && e.target.closest && e.target.closest('#bar')) {
    clearTimeout(hideTimer);
    document.body.classList.add('controls');
    return;
  }
  showControls();
});
bar.addEventListener('mouseenter', () => { clearTimeout(hideTimer); document.body.classList.add('controls') });
bar.addEventListener('mouseleave', () => { clearTimeout(hideTimer); if (!v.paused) hideTimer = setTimeout(hideControls, 1800) });
rateBtn.addEventListener('click', () => {
  rateMenu.hidden = !rateMenu.hidden;
  if (!rateMenu.hidden) { clearTimeout(hideTimer); document.body.classList.add('controls') }
});
rateMenu.addEventListener('click', e => {
  const b = e.target.closest('button');
  if (b) setRate(+b.dataset.rate);
});
document.addEventListener('pointerdown', e => {
  if (rateMenu.hidden) return;
  if (e.target.closest('#rates') || e.target.closest('#rate')) return;
  rateMenu.hidden = true;
});
v.addEventListener('ended', () => send('ended'));
v.addEventListener('playing', () => send('playing'));
v.addEventListener('waiting', () => send('waiting'));
v.addEventListener('error', () => send('error', '&msg=' + (v.error ? v.error.code : '')));
v.addEventListener('timeupdate', () => {
  const now = Date.now();
  if (now - last < 3000) return;
  last = now;
  send('progress', '&t=' + v.currentTime.toFixed(1) + '&d=' + (v.duration || 0).toFixed(1));
});
v.addEventListener('pause', () => {
  if (v.currentTime > 0) send('progress', '&t=' + v.currentTime.toFixed(1) + '&d=' + (v.duration || 0).toFixed(1));
});
document.addEventListener('keydown', e => {
  if (e.target && e.target.closest && e.target.closest('#bar')) return;
  if (e.key === ' ') { e.preventDefault(); kx.toggle() }
  else if (e.key === 'ArrowLeft') { e.preventDefault(); kx.seek(e.shiftKey ? -30 : -5) }
  else if (e.key === 'ArrowRight') { e.preventDefault(); kx.seek(e.shiftKey ? 30 : 5) }
  else if (e.key === '[') { e.preventDefault(); kx.nudgeRate(-1) }
  else if (e.key === ']') { e.preventDefault(); kx.nudgeRate(1) }
  else if (e.key === 'ArrowDown') { e.preventDefault(); send('next') }
  else if (e.key === 'ArrowUp') { e.preventDefault(); send('prev') }
});
syncPlay(); syncVolume(); syncTime(); showControls();
</script></body></html>`

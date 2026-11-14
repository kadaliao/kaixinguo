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
#bar{position:fixed;left:50%;bottom:12px;width:min(680px,calc(100% - 24px));box-sizing:border-box;display:flex;flex-direction:column;gap:4px;
  padding:8px 14px 7px;border-radius:15px;background:rgba(24,24,26,.42);-webkit-backdrop-filter:blur(18px);
  backdrop-filter:blur(18px);box-shadow:0 8px 28px rgba(0,0,0,.26);color:#fff;z-index:2;
  opacity:0;transform:translate(-50%,8px);transition:opacity .18s ease,transform .18s ease;pointer-events:none}
body.controls #bar{opacity:1;transform:translate(-50%,0);pointer-events:auto}
body:not(.controls){cursor:none}
#transport{display:flex;align-items:center;justify-content:center;gap:10px;height:36px}
#track{display:flex;align-items:center;justify-content:center;gap:8px;height:28px}
#bar .ctl{width:28px;height:28px;flex:none;padding:0;border:0;border-radius:8px;background:transparent;
  color:rgba(255,255,255,.9);display:flex;align-items:center;justify-content:center;cursor:pointer}
#transport .ctl{width:32px;height:32px;border-radius:10px}
#transport #play{width:36px;height:36px;border-radius:12px;background:rgba(255,255,255,.1)}
#transport #play:hover{background:rgba(255,255,255,.17)}
#transport #play:active{background:rgba(255,255,255,.24)}
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
#seek{flex:0 1 320px;width:320px;min-width:50px}
#vol{width:72px;flex:none}
#ratebox{position:relative;display:flex;flex:none}
#bar #rate{width:auto;min-width:46px;padding:0 6px;
  font:600 11px/1 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;font-variant-numeric:tabular-nums}
#rates{position:absolute;left:50%;bottom:calc(100% + 10px);transform:translateX(-50%);flex-direction:column;gap:2px;
  min-width:72px;padding:6px;border-radius:12px;background:rgba(24,24,26,.92);
  -webkit-backdrop-filter:blur(22px);backdrop-filter:blur(22px);box-shadow:0 10px 34px rgba(0,0,0,.38)}
#rates[hidden]{display:none}
#rates:not([hidden]){display:flex}
#rates button{height:28px;border:0;border-radius:8px;background:transparent;color:rgba(255,255,255,.9);
  font:500 12px/1 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;font-variant-numeric:tabular-nums;cursor:pointer}
#rates button:hover,#rates button.on{background:rgba(255,255,255,.14)}
#dm{position:absolute;left:0;top:0;width:0;height:0;overflow:hidden;pointer-events:none;z-index:1}
#dm .d,#dmruler{position:absolute;left:0;top:0;white-space:pre;color:#fff;opacity:.86;
  font-family:-apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;font-weight:500;line-height:1.3;
  text-shadow:0 0 1px rgba(0,0,0,.85),0 1px 2px rgba(0,0,0,.6);will-change:transform}
#dmruler{visibility:hidden}
body.dmoff #dm,body.pip #dm{display:none}
#bar #dmbtn svg text{font:600 10px -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;fill:currentColor;stroke:none}
#dmbtn .slash{display:none}
body.dmoff #dmbtn{color:rgba(255,255,255,.5)}
body.dmoff #dmbtn .slash{display:block}
</style></head>
<body><video id="v" playsinline preload="auto"></video>
<div id="dm" aria-hidden="true"><span id="dmruler"></span></div>
<div id="bar" role="group" aria-label="播放控制">
  <div id="transport" role="group" aria-label="播放与换集">
    <button id="prev" class="ctl" aria-label="上一集" title="上一集">
      <svg viewBox="0 0 24 24"><path d="M6 5v14"/><path d="M18 6.5 9.5 12 18 17.5z" fill="currentColor" stroke="none"/></svg>
    </button>
    <button id="rew" class="ctl" aria-label="快退 10 秒" title="快退 10 秒">
      <svg viewBox="0 0 24 24"><path d="M11 6 4 12l7 6zM20 6l-7 6 7 6z" fill="currentColor" stroke="none"/></svg>
    </button>
    <button id="play" class="ctl" aria-label="播放">
      <svg class="play" viewBox="0 0 24 24"><path d="M8 5.2v13.6L19 12z" fill="currentColor" stroke="none"/></svg>
      <svg class="pause" viewBox="0 0 24 24"><path d="M7 5h3.4v14H7zM13.6 5H17v14h-3.4z" fill="currentColor" stroke="none"/></svg>
    </button>
    <button id="fwd" class="ctl" aria-label="快进 10 秒" title="快进 10 秒">
      <svg viewBox="0 0 24 24"><path d="M13 6l7 6-7 6zM4 6l7 6-7 6z" fill="currentColor" stroke="none"/></svg>
    </button>
    <button id="next" class="ctl" aria-label="下一集" title="下一集">
      <svg viewBox="0 0 24 24"><path d="M18 5v14"/><path d="M6 6.5 14.5 12 6 17.5z" fill="currentColor" stroke="none"/></svg>
    </button>
  </div>
  <div id="track" role="group" aria-label="进度和音量">
    <span id="cur" class="time">0:00</span>
    <input id="seek" type="range" min="0" max="1000" step="1" value="0" aria-label="播放进度">
    <span id="dur" class="time">0:00</span>
    <button id="mute" class="ctl" aria-label="静音">
      <svg viewBox="0 0 24 24"><path d="M4 9h4l5-4v14l-5-4H4z" fill="currentColor" stroke="none"/><path class="wave" d="M16 9.2a4 4 0 0 1 0 5.6M18.6 6.8a7.5 7.5 0 0 1 0 10.4"/></svg>
    </button>
    <input id="vol" type="range" min="0" max="1" step="0.01" value="1" aria-label="音量">
    <div id="ratebox">
      <button id="rate" class="ctl" aria-label="播放速度 1.0×">1.0×</button>
      <div id="rates" role="menu" aria-label="播放速度" hidden>
        <button type="button" data-rate="0.5">0.5×</button>
        <button type="button" data-rate="0.75">0.75×</button>
        <button type="button" data-rate="1" class="on">1.0×</button>
        <button type="button" data-rate="1.25">1.25×</button>
        <button type="button" data-rate="1.5">1.5×</button>
        <button type="button" data-rate="2">2.0×</button>
      </div>
    </div>
    <button id="dmbtn" class="ctl" aria-label="关闭弹幕" title="弹幕（D）">
      <svg viewBox="0 0 24 24"><rect x="3.5" y="4.5" width="17" height="15" rx="4"/><text x="12" y="15.6" text-anchor="middle">弹</text><path class="slash" d="M5 19.5 19 4.5"/></svg>
    </button>
    <button id="pip" class="ctl" aria-label="小窗播放">
      <svg viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="2.2"/><rect x="12" y="12" width="7.5" height="5.5" rx="1.2" fill="currentColor" stroke="none"/></svg>
    </button>
  </div>
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
const prevBtn = document.getElementById('prev');
const rewBtn = document.getElementById('rew');
const fwdBtn = document.getElementById('fwd');
const nextBtn = document.getElementById('next');
const muteBtn = document.getElementById('mute');
const pipBtn = document.getElementById('pip');
const rateBtn = document.getElementById('rate');
const rateMenu = document.getElementById('rates');
const speeds = [0.5, 0.75, 1, 1.25, 1.5, 2];
let ep = '', last = 0, brightness = 1, hudTimer = 0, hideTimer = 0, seeking = false;
let rate = 1;
let hold = false, dblInterval = 500, chromeOn = false;
const hideAfter = 2000;
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
function setChrome(on) {
  if (on === chromeOn) return;
  chromeOn = on;
  document.body.classList.toggle('controls', on);
  // The player's top buttons are native, so tell Go to hide and show them
  // with this bar.
  send('chrome', on ? '&on=1' : '&on=0');
}
function showControls() {
  setChrome(true);
  clearTimeout(hideTimer);
  if (!v.paused && rateMenu.hidden) hideTimer = setTimeout(hideControls, hideAfter);
}
function hideControls() {
  rateMenu.hidden = true;
  setChrome(false);
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
// 弹幕 fly from the picture's right edge to its left, over its upper part,
// in lanes where no line catches up with the one ahead. Their clock is
// the video's: they pause, speed up and jump with it.
const dmLayer = document.getElementById('dm');
const dmRuler = document.getElementById('dmruler');
const dmBtn = document.getElementById('dmbtn');
const dmLate = 1500; // ms a line may come late and still fly
const dmWait = 2500; // ms a line waits for a free lane
const dmGap = 28; // px between two lines of a lane
let dmOn = true, dmLines = [], dmNext = 0, dmQueue = [], dmLanes = [], dmFly = new Set(), dmTimer = 0;
let dmBox = null;
function dmLayout() {
  const W = window.innerWidth, H = window.innerHeight;
  if (!W || !H) return;
  const ar = v.videoWidth > 0 && v.videoHeight > 0 ? v.videoWidth / v.videoHeight : 9 / 16;
  let w = W, h = H;
  if (W / H > ar) w = Math.round(H * ar); else h = Math.round(W / ar);
  const left = Math.round((W - w) / 2), top = Math.round((H - h) / 2);
  Object.assign(dmLayer.style, {left: left + 'px', top: top + 'px', width: w + 'px', height: h + 'px'});
  const fs = clamp(Math.round(h * 0.024), 13, 22), lh = Math.round(fs * 1.6);
  const lanes = clamp(Math.floor(h * 0.3 / lh), 3, 6);
  // Clear of the window's title bar, where the exit and toolbar sit.
  dmBox = {w, fs, lh, top: Math.max(48 - top, Math.round(h * 0.06)), dur: clamp(w * 13, 5500, 10000)};
  while (dmLanes.length < lanes) dmLanes.push({clear: -Infinity, exit: -Infinity});
  dmLanes.length = lanes;
}
function dmIndex(t) {
  let lo = 0, hi = dmLines.length;
  while (lo < hi) { const m = (lo + hi) >> 1; if (dmLines[m].t < t) lo = m + 1; else hi = m }
  return lo;
}
function dmClear() {
  for (const f of dmFly) { f.anim.cancel(); f.el.remove() }
  dmFly.clear();
  dmQueue = [];
  for (const l of dmLanes) { l.clear = -Infinity; l.exit = -Infinity }
}
// dmSeek starts over from the playhead: what is flying goes, and lines
// after it fly again.
function dmSeek() {
  dmClear();
  for (const l of dmLines) l.shown = false;
  dmNext = dmIndex(v.currentTime * 1000);
}
function dmReset() { dmClear(); dmLines = []; dmNext = 0 }
// The lines are let go on a timer rather than on frames, which the page
// stops drawing when the system deems it hidden; the flight itself is an
// animation.
function dmRun() {
  if (!dmTimer && dmShown() && !v.paused && dmLines.length) dmTimer = setInterval(dmTick, 100);
}
// dmShown reports whether 弹幕 can fly: on, and the picture in the page
// rather than in the small window.
function dmShown() { return dmOn && !document.body.classList.contains('pip') }
function dmTick() {
  if (!dmShown() || v.paused || !v.src) { clearInterval(dmTimer); dmTimer = 0; return }
  const t = v.currentTime * 1000;
  while (dmNext < dmLines.length && dmLines[dmNext].t <= t) {
    const l = dmLines[dmNext++];
    if (!l.shown && t - l.t <= dmLate) { l.shown = true; dmQueue.push(l) }
  }
  if (dmQueue.length) dmQueue = dmQueue.filter(l => t - l.t <= dmWait && !dmLaunch(l, t));
}
// dmLaunch flies l from now, t, if a lane has room, and reports whether
// it did.
function dmLaunch(l, t) {
  if (!dmBox) dmLayout();
  if (!dmBox) return false;
  const W = dmBox.w, D = dmBox.dur;
  if (l.fs !== dmBox.fs) {
    dmRuler.style.fontSize = dmBox.fs + 'px';
    dmRuler.textContent = l.s;
    l.w = dmRuler.offsetWidth; l.fs = dmBox.fs;
  }
  const speed = (W + l.w) / D; // px per ms of play
  let lane = -1;
  for (let i = 0; i < dmLanes.length; i++) {
    const n = dmLanes[i];
    // The lane's last line is all in, and this one, faster for being
    // longer, reaches the left edge only after it has gone.
    if (t >= n.clear && t + W / speed >= n.exit) { lane = i; break }
  }
  if (lane < 0) return false;
  dmLanes[lane].clear = t + (l.w + dmGap) / speed;
  dmLanes[lane].exit = t + D;
  const el = document.createElement('div');
  el.className = 'd';
  el.textContent = l.s;
  el.style.fontSize = dmBox.fs + 'px';
  el.style.top = (dmBox.top + lane * dmBox.lh) + 'px';
  dmLayer.appendChild(el);
  const anim = el.animate([{transform: 'translateX(' + W + 'px)'}, {transform: 'translateX(' + (-l.w) + 'px)'}],
    {duration: D, easing: 'linear'});
  anim.playbackRate = v.playbackRate;
  const f = {el, anim};
  dmFly.add(f);
  anim.onfinish = () => { el.remove(); dmFly.delete(f) };
  return true;
}
function dmPause() { for (const f of dmFly) f.anim.pause() }
function dmPlay() {
  for (const f of dmFly) { f.anim.playbackRate = v.playbackRate; f.anim.play() }
  dmRun();
}
window.kx = {
  play(src, id, start) {
    ep = id; last = 0; seeking = false;
    dmReset();
    v.src = src;
    applyRate(); // a new src can clear the element's rate
    if (start > 0) v.addEventListener('loadedmetadata', () => { v.currentTime = start }, {once: true});
    if (!hold) v.play().catch(e => { if (e.name !== 'AbortError') send('error', '&msg=' + encodeURIComponent(e.message)) });
  },
  stop() { v.pause(); v.removeAttribute('src'); v.load(); dmReset() },
  // danmaku takes lines of episode id, each [ms, text]; they may come in
  // any order and while the episode plays.
  danmaku(id, lines) {
    if (id !== ep || !lines || !lines.length) return;
    dmLines = dmLines.concat(lines.map(l => ({t: l[0], s: l[1], w: 0, fs: 0, shown: false})));
    dmLines.sort((a, b) => a.t - b.t);
    // Lines that just came due still fly.
    dmNext = dmIndex(v.currentTime * 1000 - dmLate);
    dmRun();
  },
  setDanmaku(on, quiet) {
    on = !!on;
    if (on === dmOn) return;
    dmOn = on;
    document.body.classList.toggle('dmoff', !on);
    dmBtn.setAttribute('aria-label', on ? '关闭弹幕' : '打开弹幕');
    if (on) { dmSeek(); dmRun() } else dmClear();
    if (!quiet) { showHud(on ? '弹幕已开启' : '弹幕已关闭'); showControls() }
  },
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
prevBtn.addEventListener('click', () => { send('prev'); showHud('上一集') });
nextBtn.addEventListener('click', () => { send('next'); showHud('下一集') });
rewBtn.addEventListener('click', () => kx.seek(-10));
fwdBtn.addEventListener('click', () => kx.seek(10));
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
    setChrome(true);
    return;
  }
  showControls();
});
bar.addEventListener('mouseenter', () => { clearTimeout(hideTimer); setChrome(true) });
bar.addEventListener('mouseleave', () => { clearTimeout(hideTimer); if (!v.paused) hideTimer = setTimeout(hideControls, hideAfter) });
rateBtn.addEventListener('click', () => {
  rateMenu.hidden = !rateMenu.hidden;
  if (!rateMenu.hidden) { clearTimeout(hideTimer); setChrome(true) }
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
v.addEventListener('loadedmetadata', dmLayout);
window.addEventListener('resize', dmLayout);
v.addEventListener('playing', dmPlay);
v.addEventListener('pause', dmPause);
v.addEventListener('waiting', dmPause);
v.addEventListener('seeking', dmSeek);
// Tell the app where a seek landed now, not at the next progress, so
// the 弹幕 there are read.
v.addEventListener('seeked', () => { last = 0 });
v.addEventListener('ratechange', () => { for (const f of dmFly) f.anim.playbackRate = v.playbackRate });
// The small window shows only the picture; the 弹幕 wait for it to come
// back.
function dmPip(on) {
  if (on === document.body.classList.contains('pip')) return;
  document.body.classList.toggle('pip', on);
  if (on) dmClear(); else { dmSeek(); dmRun() }
}
v.addEventListener('enterpictureinpicture', () => dmPip(true));
v.addEventListener('leavepictureinpicture', () => dmPip(false));
v.addEventListener('webkitpresentationmodechanged', () => dmPip(v.webkitPresentationMode === 'picture-in-picture'));
dmBtn.addEventListener('click', () => {
  const on = !dmOn;
  kx.setDanmaku(on);
  send('danmaku', '&on=' + (on ? 1 : 0));
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
  else if ((e.key === 'd' || e.key === 'D') && !e.metaKey && !e.ctrlKey && !e.altKey) { e.preventDefault(); dmBtn.click() }
  else if (e.key === 'ArrowDown') { e.preventDefault(); send('next') }
  else if (e.key === 'ArrowUp') { e.preventDefault(); send('prev') }
});
syncPlay(); syncVolume(); syncTime(); showControls();
</script></body></html>`

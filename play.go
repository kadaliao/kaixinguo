package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/egoist/mygo"
)

// playback is the episode the player shows. It belongs to the main thread.
type playback struct {
	seriesID string
	number   int
	start    float64 // where to start, in seconds
	gen      int     // tells this start from later ones
	state    playState
	msg      string
	pending  bool // waits for the series to load
}

type playState int

const (
	playResolving playState = iota // finding the episode's file
	playBuffering                  // the video loads
	playPlaying
	playFailed
)

// future is an episode's playback model being resolved, shared by the
// start of the episode and the prefetch before it.
type future struct {
	done chan struct{}
	pb   *model.Playback
	err  error
}

// resolve returns the episode's playback model, resolving it unless a
// prefetch did or is doing it.
func (a *app) resolve(ep model.Episode) *future {
	if f := a.futures[ep.ID]; f != nil {
		select {
		case <-f.done:
			if f.err == nil && !expired(f.pb) {
				return f
			}
		default:
			return f
		}
	}
	f := &future{done: make(chan struct{})}
	a.futures[ep.ID] = f
	go func() {
		defer close(f.done)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		f.pb, f.err = a.client.ResolvePlayback(ctx, ep.ID, sdk.PlaybackOptions{})
		if errors.Is(f.err, sdk.ErrDRMProtected) && f.pb != nil {
			f.err = nil // ContentKey decrypts it
		}
	}()
	return f
}

// expired reports a model whose CDN URLs expire within a minute.
func expired(pb *model.Playback) bool {
	return pb == nil || pb.ExpiresAt > 0 && time.Unix(pb.ExpiresAt, 0).Before(time.Now().Add(time.Minute))
}

// play opens the player on episode n of the series, from start seconds in.
func (a *app) play(seriesID string, n int, start float64) {
	a.gen++
	a.cur = &playback{seriesID: seriesID, number: n, start: start, gen: a.gen}
	switch path := a.router.Path(); {
	case path == "/play/"+seriesID:
	case strings.HasPrefix(path, "/play/"):
		// Another season, in the same place: Back leaves the player.
		a.router.Replace("/play/" + seriesID)
	default:
		a.router.Push("/play/" + seriesID)
	}
	a.ensureWebView()
	a.evalJS("kx.stop()")
	if a.playerTutorialOn {
		a.evalJS("kx.setHold(true)") // before the episode resolves and plays
	}
	sp := a.seriesPage(seriesID)
	if sp.series == nil {
		a.cur.pending = true // fillPending starts it
		return
	}
	a.startEpisode(sp)
}

// fillPending starts the episode that waited for its series.
func (a *app) fillPending(sp *seriesPage) {
	if p := a.cur; p != nil && p.pending && p.seriesID == sp.id {
		p.pending = false
		a.startEpisode(sp)
	}
}

func (a *app) startEpisode(sp *seriesPage) {
	p := a.cur
	ep, ok := sp.episode(p.number)
	if !ok {
		p.state, p.msg = playFailed, fmt.Sprintf("没有第 %d 集", p.number)
		return
	}
	if !ep.Playable {
		p.state, p.msg = playFailed, "这一集需要登录才能观看"
		return
	}
	if sp.series != nil && sp.series.Platform == model.PlatformUnknown {
		p.state, p.msg = playFailed, "这是小说或有声书，不能播放"
		return
	}
	p.state, p.msg = playResolving, ""
	a.recordWatch(sp, p.number, p.start, 0, false)
	gen, start := p.gen, p.start
	f := a.resolve(ep)
	go func() {
		<-f.done
		src, err := a.streamURL(f)
		a.update(func() {
			if a.cur == nil || a.cur.gen != gen {
				return
			}
			if err != nil {
				a.cur.state, a.cur.msg = playFailed, friendly(err)
				delete(a.futures, ep.ID) // try afresh next time
				return
			}
			a.cur.state = playBuffering
			a.evalJS(fmt.Sprintf("kx.play(%q,%q,%s)", src, ep.ID, strconv.FormatFloat(start, 'f', 1, 64)))
		})
	}()
}

// streamURL picks the best stream of the model and gives the proxy its
// key.
func (a *app) streamURL(f *future) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	pb := f.pb
	s, err := pb.Select("")
	if err != nil {
		return "", err
	}
	if s.HLS || s.Dash || pb.AudioURL != "" {
		return "", errors.New("暂不支持这种视频格式")
	}
	var key []byte
	if pb.Protected || s.Protected {
		k, err := sdk.ContentKey(pb)
		if err != nil {
			return "", err
		}
		if key, err = hex.DecodeString(k.Hex); err != nil {
			return "", err
		}
	}
	urls := []string{s.URL}
	if s.BackupURL != "" && s.BackupURL != s.URL {
		urls = append(urls, s.BackupURL)
	}
	return a.proxy.add(urls, key), nil
}

// step plays the episode d after the one playing, if there is one.
func (a *app) step(d int) {
	p := a.cur
	if p == nil {
		return
	}
	sp := a.series[p.seriesID]
	if sp == nil {
		return
	}
	if d > 0 {
		if id, n, ok := a.following(sp, p.number); ok {
			a.play(id, n, 0)
		}
		return
	}
	if _, ok := sp.episode(p.number + d); ok {
		a.play(p.seriesID, p.number+d, 0)
	}
}

// onPlayerEvent hears what the player page's video did.
func (a *app) onPlayerEvent(q url.Values) {
	p := a.cur
	if p == nil {
		return
	}
	sp := a.series[p.seriesID]
	// Controls belong to the player, not the episode that sent them:
	// between stop and the next play the page still names the old one.
	switch q.Get("type") {
	case "theater":
		a.toggleTheater()
		return
	case "pip":
		if a.web != nil {
			a.web.Page().TogglePictureInPicture()
		}
		return
	case "chrome":
		a.playerChrome = q.Get("on") != "0"
		return
	case "next":
		a.step(1)
		return
	case "prev":
		a.step(-1)
		return
	}
	ep, _ := sp.episode(p.number)
	if id := q.Get("ep"); id != "" && id != ep.ID {
		return // an episode that played before
	}
	switch q.Get("type") {
	case "playing":
		p.state = playPlaying
		// Resolve the next episode while this one plays.
		if next, ok := sp.episode(p.number + 1); ok && next.Playable {
			a.resolve(next)
		} else if ns, ok := sp.nextSeason(); ok {
			a.seriesPage(ns.SeriesID) // its episodes, ready to go on
		}
	case "waiting":
		if p.state == playPlaying {
			p.state = playBuffering
		}
	case "progress":
		t, _ := strconv.ParseFloat(q.Get("t"), 64)
		d, _ := strconv.ParseFloat(q.Get("d"), 64)
		a.recordWatch(sp, p.number, t, d, false)
	case "ended":
		a.recordWatch(sp, p.number, 0, 0, true)
		// On to the next episode, or the next season's first.
		if id, n, ok := a.following(sp, p.number); ok {
			a.play(id, n, 0)
		}
	case "error":
		p.state, p.msg = playFailed, "视频加载失败，请重试"
		delete(a.futures, ep.ID)
	}
}

func (a *app) recordWatch(sp *seriesPage, n int, pos, dur float64, finished bool) {
	if sp == nil || sp.series == nil {
		return
	}
	w, _ := a.hist.get(sp.id)
	w.SeriesID, w.Title, w.Cover = sp.id, sp.series.Title, sp.series.CoverURL
	w.Episode, w.Position, w.Duration, w.Total = n, pos, dur, len(sp.episodes)
	if season := sp.seasonIndex(); season > 0 {
		w.Season = season
	}
	w.Finished = finished
	w.Want = false
	// The store writes snapshots in the background; copy the map so this
	// update cannot race that write or change a snapshot after the fact.
	eps := make(map[int]episodeWatch, len(w.Episodes)+1)
	for i, ew := range w.Episodes {
		eps[i] = ew
	}
	w.Episodes = eps
	e := w.Episodes[n]
	e.Position = pos
	if dur > 0 {
		e.Duration = dur // a start of the episode has no duration yet
	}
	if finished || dur > 0 && pos >= dur*0.98 {
		e.Finished = true
	}
	w.Episodes[n] = e
	// A whole series is done when the last episode ends, or when every
	// episode has been watched.
	if finished && w.Total > 0 && n >= w.Total {
		w.Done = true
	}
	watched := 0
	for _, ew := range w.Episodes {
		if ew.Finished {
			watched++
		}
	}
	if w.Total > 0 && watched >= w.Total {
		w.Done = true
	}
	a.hist.record(w)
}

// resumePoint is where to go on from: the series (this season or the
// next), the episode and the second.
func (a *app) resumePoint(sp *seriesPage) (id string, n int, start float64, label string) {
	w, ok := a.hist.get(sp.id)
	if !ok || w.Episode <= 0 || len(sp.episodes) == 0 {
		return sp.id, 1, 0, "播放第 1 集"
	}
	if w.Finished {
		if _, ok := sp.episode(w.Episode + 1); ok {
			return sp.id, w.Episode + 1, 0, fmt.Sprintf("播放第 %d 集", w.Episode+1)
		}
		if ns, ok := sp.nextSeason(); ok {
			return ns.SeriesID, 1, 0, "播放" + seasonLabel(ns)
		}
		return sp.id, 1, 0, "从头再看"
	}
	return sp.id, w.Episode, max(0, w.Position-3), fmt.Sprintf("继续看第 %d 集", w.Episode)
}

// ensureWebView makes the player's web view, once.
func (a *app) ensureWebView() {
	if a.web != nil || a.win == nil {
		return
	}
	web, err := a.win.NewWebView(mygo.WebViewOptions{
		URL:             a.proxy.playerURL(),
		BackgroundColor: "#000000",
		Page: mygo.PageOptions{
			Autoplay:         mygo.AutoplayAllow,
			PictureInPicture: true,
		},
	})
	if err != nil {
		if a.cur != nil {
			a.cur.state, a.cur.msg = playFailed, "无法创建播放器："+err.Error()
		}
		return
	}
	a.web = web
	a.js = make(chan string, 16)
	go func(page *mygo.Page, q <-chan string) {
		for js := range q {
			_, _ = page.Eval(js) // one at a time, in order
		}
	}(web.Page(), a.js)
	web.Page().OnDidFinishLoad(func() {
		a.update(func() {
			a.webReady = true
			pending := a.pendingJS
			a.pendingJS = nil
			for _, js := range pending {
				a.evalJS(js)
			}
		})
	})
}

// evalJS runs js in the player page once it has loaded.
func (a *app) evalJS(js string) {
	if a.web == nil {
		return
	}
	if !a.webReady {
		a.pendingJS = append(a.pendingJS, js)
		return
	}
	select {
	case a.js <- js:
	default: // the page is stuck; drop it rather than block the UI
	}
}

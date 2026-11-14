package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
)

// 弹幕 are the lines other viewers left on an episode, which fly across the
// picture as it plays. The service hands them out a window of play time at
// a time (30 seconds as of 2026-11), so the app reads them as the episode
// plays, about a minute ahead of the playhead, and gives each window's
// lines to the player page, which flies them in time with the video.

const (
	danmakuAhead    = 60_000 // ms of play time to have read past the playhead
	danmakuWindow   = 30_000 // ms a window covers when the service doesn't say
	danmakuRepeat   = 20_000 // ms within which the same line shows once
	danmakuMaxRunes = 40     // longer lines are cut, with an ellipsis
	danmakuTracks   = 6      // episodes whose lines stay in memory
	danmakuFails    = 3      // failed reads in a row before an episode gives up
	danmakuOffFile  = "danmaku-off"
	danmakuEnd      = math.MaxInt64
)

// bullet is a line the page flies: when it shows, in ms of play time, and
// what it says. It goes to the page as [at, "text"].
type bullet struct {
	at   int64
	text string
}

func (b bullet) MarshalJSON() ([]byte, error) { return json.Marshal([]any{b.at, b.text}) }

// span is play time read, from and to in ms, to excluded.
type span struct{ from, to int64 }

// danmakuTrack is what the app has read of an episode's 弹幕. It belongs
// to the main thread.
type danmakuTrack struct {
	seriesID, epID string

	lines []bullet           // cleaned, in the order they were read
	ids   map[string]bool    // lines read, by the service's ID
	kept  map[string][]int64 // when each text shows, to drop its repeats
	spans []span             // play time read, sorted, apart

	// cursor reads on from next, where the last window read ends.
	cursor string
	next   int64

	busy    bool
	fails   int
	retryAt time.Time
}

func newDanmakuTrack(seriesID, epID string) *danmakuTrack {
	return &danmakuTrack{seriesID: seriesID, epID: epID, ids: map[string]bool{}, kept: map[string][]int64{}}
}

// want is the read that brings the track closer to a minute past t, if
// one is due.
func (tr *danmakuTrack) want(t int64) (sdk.DanmakuOptions, bool) {
	t = max(0, t)
	s, ok := tr.spanAt(t)
	switch {
	case !ok:
		return sdk.DanmakuOptions{StartMs: t}, true
	case s.to == danmakuEnd || s.to-t >= danmakuAhead:
		return sdk.DanmakuOptions{}, false
	case tr.cursor != "" && tr.next == s.to:
		return sdk.DanmakuOptions{Cursor: tr.cursor}, true
	default:
		return sdk.DanmakuOptions{StartMs: s.to}, true
	}
}

// spanAt is the read span t falls in.
func (tr *danmakuTrack) spanAt(t int64) (span, bool) {
	for _, s := range tr.spans {
		if s.from <= t && t < s.to {
			return s, true
		}
	}
	return span{}, false
}

// add takes in the window that the read opts brought.
func (tr *danmakuTrack) add(p *model.DanmakuPage, opts sdk.DanmakuOptions) {
	asked := opts.StartMs
	if opts.Cursor != "" {
		asked = tr.next
	}
	// The window always covers where it was asked from, so a read never
	// repeats forever, even when the service's bounds are off.
	from, to := min(p.StartMS, asked), p.EndMS
	if to <= asked {
		to = asked + danmakuWindow
	}
	if !p.HasMore {
		to = danmakuEnd // the rest of the episode
	}
	tr.cover(span{from, to})
	if p.HasMore && p.Cursor != "" {
		tr.cursor, tr.next = p.Cursor, to
	} else {
		tr.cursor, tr.next = "", 0
	}

	for _, d := range p.Items {
		if d.ID != "" {
			if tr.ids[d.ID] {
				continue
			}
			tr.ids[d.ID] = true
		}
		text := cleanDanmaku(d.Text)
		if text == "" || tr.repeats(text, d.OffsetMS) {
			continue
		}
		tr.kept[text] = append(tr.kept[text], d.OffsetMS)
		tr.lines = append(tr.lines, bullet{at: max(0, d.OffsetMS), text: text})
	}
}

// repeats reports a text that already shows within a few seconds of at:
// viewers' "+1"s come as copies of the line they agree with.
func (tr *danmakuTrack) repeats(text string, at int64) bool {
	for _, o := range tr.kept[text] {
		if d := at - o; d > -danmakuRepeat && d < danmakuRepeat {
			return true
		}
	}
	return false
}

// cover adds s to the spans read, merging those it meets.
func (tr *danmakuTrack) cover(s span) {
	out := tr.spans[:0:0]
	for _, o := range tr.spans {
		if o.to < s.from || s.to < o.from {
			out = append(out, o)
			continue
		}
		s.from, s.to = min(s.from, o.from), max(s.to, o.to)
	}
	i := 0
	for i < len(out) && out[i].from < s.from {
		i++
	}
	tr.spans = append(out[:i], append([]span{s}, out[i:]...)...)
}

// danmakuEmoji are the service's emoji codes, as the system's emoji. Codes
// not here show as they are.
var danmakuEmoji = map[string]string{
	"[笑哭]": "😂", "[赞]": "👍", "[鼓掌]": "👏", "[送花]": "💐", "[偷笑]": "🤭",
	"[捂脸]": "🤦", "[爱慕]": "🥰", "[爽]": "😎", "[爱心]": "❤️", "[奸笑]": "😏",
	"[没看够]": "🥺", "[笑]": "😄", "[大笑]": "😆", "[微笑]": "🙂", "[快哭了]": "🥹",
	"[害羞]": "😊", "[怒]": "😠", "[哭]": "😭", "[流泪]": "😭", "[呲牙]": "😁",
	"[色]": "😍", "[可怜]": "🥺", "[吃瓜]": "🍉", "[强]": "👍", "[抱拳]": "🙏",
	"[比心]": "💕", "[玫瑰]": "🌹", "[惊讶]": "😮", "[震惊]": "😱", "[生气]": "😠",
	"[疑问]": "❓", "[再见]": "👋", "[OK]": "👌", "[胜利]": "✌️", "[握手]": "🤝",
	"[加油]": "💪", "[飞吻]": "😘", "[亲亲]": "😚", "[擦汗]": "😅", "[尬笑]": "😅",
	"[憨笑]": "😄", "[难过]": "😞", "[委屈]": "🥺", "[发怒]": "😡", "[愤怒]": "😡",
	"[心碎]": "💔", "[火]": "🔥", "[666]": "666", "[灵机一动]": "💡", "[泣不成声]": "😭",
}

// cleanDanmaku is a line as it shows: one line of plain text, emoji codes
// made emoji, cut when long. It is empty for a line with nothing to show.
func cleanDanmaku(s string) string {
	if strings.Contains(s, "[") {
		s = replaceEmoji(s)
	}
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			continue
		case unicode.IsSpace(r) || unicode.IsControl(r):
			space = b.Len() > 0
			continue
		case unicode.Is(unicode.Cf, r) && r != '\u200d':
			// Direction overrides and other invisibles, but not the
			// joiner inside emoji.
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > danmakuMaxRunes {
		out = string([]rune(out)[:danmakuMaxRunes-1]) + "…"
	}
	return out
}

func replaceEmoji(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '[')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], ']')
		if j < 0 {
			break
		}
		code := s[i : i+j+1]
		b.WriteString(s[:i])
		if e, ok := danmakuEmoji[code]; ok {
			b.WriteString(e)
		} else {
			b.WriteString(code)
		}
		s = s[i+j+1:]
	}
	b.WriteString(s)
	return b.String()
}

// danmakuJS hands the page lines of episode id.
func danmakuJS(id string, lines []bullet) string {
	data, _ := json.Marshal(lines) // escapes U+2028 and U+2029 for the script
	return fmt.Sprintf("kx.danmaku(%q,%s)", id, data)
}

// danmakuOn reports whether 弹幕 show: they do unless they were turned
// off on this Mac.
func danmakuOn(dir string) bool { return !markerSeen(dir, danmakuOffFile) }

// setDanmaku shows or hides 弹幕, here and on later launches.
func (a *app) setDanmaku(on bool) {
	a.danmakuOn = on
	if on {
		_ = os.Remove(filepath.Join(a.dataDir, danmakuOffFile))
	} else {
		writeMarker(a.dataDir, danmakuOffFile)
	}
	a.evalJS(fmt.Sprintf("kx.setDanmaku(%t)", on))
	if on {
		a.feedDanmaku()
	}
}

// danmakuTrack is episode epID's track, made the first time it is asked
// for. Only the last few episodes keep theirs.
func (a *app) danmakuTrack(seriesID, epID string) *danmakuTrack {
	if tr := a.tracks[epID]; tr != nil {
		return tr
	}
	tr := newDanmakuTrack(seriesID, epID)
	a.tracks[epID] = tr
	a.trackOrder = append(a.trackOrder, epID)
	for len(a.trackOrder) > danmakuTracks {
		delete(a.tracks, a.trackOrder[0])
		a.trackOrder = a.trackOrder[1:]
	}
	return tr
}

// feedDanmaku reads the 弹幕 of the episode playing until what is read
// reaches a minute past the playhead, one window at a time.
func (a *app) feedDanmaku() {
	p := a.cur
	if !a.danmakuOn || p == nil || p.epID == "" || a.listDanmaku == nil {
		return
	}
	tr := a.danmakuTrack(p.seriesID, p.epID)
	if tr.busy || tr.fails >= danmakuFails || time.Now().Before(tr.retryAt) {
		return
	}
	opts, ok := tr.want(p.at)
	if !ok {
		return
	}
	tr.busy = true
	list := a.listDanmaku
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		page, err := list(ctx, tr.seriesID, tr.epID, opts)
		a.update(func() {
			tr.busy = false
			if err != nil {
				// 弹幕 are an extra: try again a little later, and give up
				// on the episode after a few failures.
				tr.fails++
				tr.retryAt = time.Now().Add(time.Duration(tr.fails) * 10 * time.Second)
				return
			}
			tr.fails = 0
			tr.add(page, opts)
			a.flushDanmaku()
			a.feedDanmaku()
		})
	}()
}

// flushDanmaku gives the page the lines of the episode it plays that it
// does not have yet.
func (a *app) flushDanmaku() {
	p := a.cur
	if p == nil || !p.onPage {
		return
	}
	tr := a.tracks[p.epID]
	if tr == nil || p.dmSent >= len(tr.lines) {
		return
	}
	a.evalJS(danmakuJS(p.epID, tr.lines[p.dmSent:]))
	p.dmSent = len(tr.lines)
}

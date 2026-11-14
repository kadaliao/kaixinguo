package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/egoist/mygo/ui"
)

// fakeDanmaku serves an episode of length ms in 30 second windows, one
// line every 5 seconds, as the service does: a read from a position starts
// at the window holding it, and a cursor goes on to the next window.
type fakeDanmaku struct {
	length int64
	reads  []sdk.DanmakuOptions
}

func (f *fakeDanmaku) list(_ context.Context, _, _ string, opts sdk.DanmakuOptions) (*model.DanmakuPage, error) {
	f.reads = append(f.reads, opts)
	start := opts.StartMs / 30_000 * 30_000
	if opts.Cursor != "" {
		start = mustAtoi(opts.Cursor)
	}
	if start >= f.length {
		return &model.DanmakuPage{Items: []model.Danmaku{}, StartMS: opts.StartMs}, nil
	}
	end := min(start+30_000, f.length)
	p := &model.DanmakuPage{StartMS: start, EndMS: end}
	for at := start; at < end; at += 5_000 {
		p.Items = append(p.Items, model.Danmaku{ID: itoa(int(at)), Text: "第 " + itoa(int(at/1000)) + " 秒", OffsetMS: at})
	}
	if end < f.length {
		p.HasMore, p.Cursor = true, itoa(int(end))
	}
	return p, nil
}

func mustAtoi(s string) int64 {
	var n int64
	for _, r := range s {
		n = n*10 + int64(r-'0')
	}
	return n
}

// readAround does the reads a track wants with the playhead at t, as
// feedDanmaku does one at a time, and returns how many it did.
func readAround(t *testing.T, tr *danmakuTrack, f *fakeDanmaku, at int64) int {
	t.Helper()
	n := 0
	for {
		opts, ok := tr.want(at)
		if !ok {
			return n
		}
		if n++; n > 20 {
			t.Fatalf("still reading after %d reads; spans %v", n, tr.spans)
		}
		p, err := f.list(context.Background(), "1", "e1", opts)
		if err != nil {
			t.Fatal(err)
		}
		tr.add(p, opts)
	}
}

// A track reads a minute ahead of the playhead, not the whole episode at
// once, and goes on from its cursor as the episode plays.
func TestDanmakuReadsAhead(t *testing.T) {
	f := &fakeDanmaku{length: 10 * 60_000}
	tr := newDanmakuTrack("1", "e1")
	if n := readAround(t, tr, f, 0); n != 2 {
		t.Fatalf("%d reads at the start, want 2 (0–60 s); reads %v", n, f.reads)
	}
	if f.reads[0].StartMs != 0 || f.reads[0].Cursor != "" || f.reads[1].Cursor == "" {
		t.Fatalf("reads %+v, want a start and then its cursor", f.reads)
	}
	if n := readAround(t, tr, f, 20_000); n != 1 {
		t.Fatalf("%d reads at 20 s, want 1", n)
	}
	if got := len(tr.lines); got != 18 {
		t.Fatalf("%d lines after 90 s read, want 18", got)
	}
	if len(tr.spans) != 1 || tr.spans[0] != (span{0, 90_000}) {
		t.Fatalf("spans %v, want one of 0–90 s", tr.spans)
	}
}

// A seek past what is read reads from there; going back fills the gap,
// and nothing is read twice.
func TestDanmakuSeek(t *testing.T) {
	f := &fakeDanmaku{length: 10 * 60_000}
	tr := newDanmakuTrack("1", "e1")
	readAround(t, tr, f, 0)
	f.reads = nil
	readAround(t, tr, f, 400_000)
	if len(f.reads) == 0 || f.reads[0].StartMs != 400_000 {
		t.Fatalf("reads %+v after a seek to 400 s", f.reads)
	}
	if _, ok := tr.spanAt(400_000); !ok {
		t.Fatalf("400 s not read: %v", tr.spans)
	}
	readAround(t, tr, f, 30_000)
	seen := map[int64]bool{}
	for _, l := range tr.lines {
		if seen[l.at] {
			t.Fatalf("line at %d ms twice", l.at)
		}
		seen[l.at] = true
	}
}

// The episode's last window says there is no more: nothing past it is
// read, however far the playhead goes.
func TestDanmakuStopsAtTheEnd(t *testing.T) {
	f := &fakeDanmaku{length: 75_000}
	tr := newDanmakuTrack("1", "e1")
	readAround(t, tr, f, 0)
	if n := readAround(t, tr, f, 70_000); n != 1 {
		t.Fatalf("%d reads near the end, want 1", n)
	}
	if n := readAround(t, tr, f, 74_000); n != 0 {
		t.Fatalf("%d reads after the end was read", n)
	}
	if _, ok := tr.want(5 * 60_000); ok {
		t.Fatal("a read is wanted past the episode's end")
	}
}

// A window whose bounds the service leaves out still counts as read, so
// the same read is not asked for again.
func TestDanmakuWindowWithoutBounds(t *testing.T) {
	tr := newDanmakuTrack("1", "e1")
	opts, _ := tr.want(10_000)
	tr.add(&model.DanmakuPage{HasMore: true, Cursor: "c"}, opts)
	again, ok := tr.want(10_000)
	if !ok || again.Cursor != "c" {
		t.Fatalf("after a window without bounds the track wants %+v, %v", again, ok)
	}
}

// "+1"s come as copies of the line they agree with; a copy within a few
// seconds shows once, while the same words later on show again.
func TestDanmakuRepeatsShowOnce(t *testing.T) {
	tr := newDanmakuTrack("1", "e1")
	tr.add(&model.DanmakuPage{EndMS: 30_000, HasMore: true, Cursor: "c", Items: []model.Danmaku{
		{ID: "1", Text: "真轻松[笑哭]", OffsetMS: 4506},
		{ID: "2", Text: "真轻松[笑哭]", OffsetMS: 4507},
		{ID: "3", Text: " 真轻松[笑哭] ", OffsetMS: 6687},
		{ID: "4", Text: "真轻松[笑哭]", OffsetMS: 30_000},
		{ID: "1", Text: "真轻松[笑哭]", OffsetMS: 4506},
		{ID: "5", Text: "\u202e\n", OffsetMS: 9000},
	}}, sdk.DanmakuOptions{})
	var got []string
	for _, l := range tr.lines {
		got = append(got, itoa(int(l.at))+" "+l.text)
	}
	if want := "4506 真轻松😂|30000 真轻松😂"; strings.Join(got, "|") != want {
		t.Fatalf("lines %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestCleanDanmaku(t *testing.T) {
	long := strings.Repeat("哈", 60)
	for in, want := range map[string]string{
		"[赞][赞]太好看了":          "👍👍太好看了",
		"[不认识]的表情":            "[不认识]的表情",
		"未闭合[笑哭":              "未闭合[笑哭",
		"  第一行\n第二行\t ":       "第一行 第二行",
		"abc\u202edef":        "abcdef",
		"👨\u200d👩\u200d👧 一家人": "👨\u200d👩\u200d👧 一家人",
		"\x00\x01":            "",
		long:                  strings.Repeat("哈", danmakuMaxRunes-1) + "…",
	} {
		if got := cleanDanmaku(in); got != want {
			t.Errorf("cleanDanmaku(%q) = %q, want %q", in, got, want)
		}
	}
}

// The lines go to the page as a script the page runs: a quote, a line
// separator or a closing tag in a line must not break out of it.
func TestDanmakuJS(t *testing.T) {
	js := danmakuJS("e1", []bullet{{4506, `他说"走"</script>`}, {9000, "a\u2028b"}})
	if !strings.HasPrefix(js, `kx.danmaku("e1",[[4506,`) {
		t.Fatalf("script %s", js)
	}
	for _, bad := range []string{"</script>", "\u2028", `"走"`} {
		if strings.Contains(js, bad) {
			t.Fatalf("script %s carries %q as is", js, bad)
		}
	}
}

// 弹幕 show by default; turning them off holds on this Mac, and the
// page's button and the D key both turn them off and on again.
func TestDanmakuToggle(t *testing.T) {
	a, tt := playerApp(t)
	if !a.danmakuOn {
		t.Fatal("弹幕 are off on a first launch")
	}
	a.onPlayerEvent(url.Values{"type": {"danmaku"}, "on": {"0"}, "ep": {"e9"}})
	if a.danmakuOn || danmakuOn(a.dataDir) {
		t.Fatal("the page's button did not turn 弹幕 off for good")
	}
	if b := newApp(a.dataDir, t.TempDir()); b.danmakuOn {
		t.Fatal("弹幕 are back on after a relaunch")
	}
	tt.Key(0, ui.KeyD)
	tt.Advance(time.Second)
	if !a.danmakuOn || !danmakuOn(a.dataDir) {
		t.Fatal("D did not turn 弹幕 back on")
	}
	if !tt.HasText("D 弹幕") {
		t.Fatalf("the player's keys do not name D: %q", tt.Texts())
	}
}

// The page gets an episode's lines once it was told to play the episode,
// each line once, and never another episode's.
func TestDanmakuFlush(t *testing.T) {
	a := testApp(t)
	a.cur = &playback{seriesID: "1", number: 1, gen: 1, epID: "e1"}
	tr := a.danmakuTrack("1", "e1")
	tr.add(&model.DanmakuPage{EndMS: 30_000, HasMore: true, Cursor: "c", Items: []model.Danmaku{{ID: "1", Text: "好", OffsetMS: 1000}}}, sdk.DanmakuOptions{})
	a.flushDanmaku()
	if a.cur.dmSent != 0 {
		t.Fatal("lines went to the page before it played the episode")
	}
	a.cur.onPage = true
	a.flushDanmaku()
	if a.cur.dmSent != 1 {
		t.Fatalf("%d lines sent once the episode plays, want 1", a.cur.dmSent)
	}
	a.flushDanmaku()
	if a.cur.dmSent != 1 {
		t.Fatal("lines were sent twice")
	}
	a.cur = &playback{seriesID: "1", number: 2, gen: 2, epID: "e2", onPage: true}
	a.flushDanmaku()
	if a.cur.dmSent != 0 {
		t.Fatal("the next episode got the last one's lines")
	}
}

// Only the last few episodes keep their lines.
func TestDanmakuTracksAreBounded(t *testing.T) {
	a := testApp(t)
	for i := 1; i <= danmakuTracks+3; i++ {
		a.danmakuTrack("1", "e"+itoa(i))
	}
	if len(a.tracks) != danmakuTracks || a.tracks["e1"] != nil || a.tracks["e"+itoa(danmakuTracks+3)] == nil {
		t.Fatalf("%d tracks kept", len(a.tracks))
	}
}

// A failed read is tried again later, and an episode gives up after a few.
func TestDanmakuGivesUp(t *testing.T) {
	a := testApp(t)
	a.cur = &playback{seriesID: "1", number: 1, gen: 1, epID: "e1"}
	reads := 0
	a.listDanmaku = func(context.Context, string, string, sdk.DanmakuOptions) (*model.DanmakuPage, error) {
		reads++
		return nil, errors.New("offline")
	}
	tr := a.danmakuTrack("1", "e1")
	tr.fails = danmakuFails
	a.feedDanmaku()
	if reads != 0 || tr.busy {
		t.Fatal("an episode that gave up read again")
	}
	tr.fails, tr.retryAt = 1, time.Now().Add(time.Minute)
	a.feedDanmaku()
	if reads != 0 || tr.busy {
		t.Fatal("a read was tried before its retry time")
	}
	a.danmakuOn = false
	tr.fails, tr.retryAt = 0, time.Time{}
	a.feedDanmaku()
	if tr.busy {
		t.Fatal("弹幕 were read while off")
	}
}

// The player page has the 弹幕 layer and its switch in the bottom bar.
func TestPlayerPageHasDanmaku(t *testing.T) {
	for _, want := range []string{`id="dm"`, `id="dmbtn"`, `title="弹幕（D）"`, "danmaku(id, lines)", "setDanmaku(on, quiet)",
		"send('danmaku'", "e.key === 'd'", "dmPause", "body.dmoff #dm"} {
		if !strings.Contains(playerHTML, want) {
			t.Fatalf("the player page has no %s", want)
		}
	}
	if strings.Index(playerHTML, `id="dmbtn"`) > strings.Index(playerHTML, `id="pip"`) {
		t.Fatal("the 弹幕 switch is not before the small window button")
	}
}

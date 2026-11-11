package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/egoist/mygo/ui"
)

// playerApp is the test app playing its first episode, without a web view
// or a window.
func playerApp(t *testing.T) (*app, *ui.Tester) {
	a := testApp(t)
	a.cur = &playback{seriesID: "1", number: 1, gen: 1}
	a.router.Push("/play/1")
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)
	return a, tt
}

// The theater fills the screen: the episodes leave the side and come back
// from the right edge, out only when the pointer nears it.
func TestPlayerTheater(t *testing.T) {
	a, tt := playerApp(t)
	for _, s := range []string{"退出播放", "影院", "第 1 集 / 共 130 集"} {
		if !tt.HasText(s) {
			t.Fatalf("no %q in %q", s, tt.Texts())
		}
	}
	click(t, tt, "影院")
	if !a.fullscreen || !a.theaterShown || a.panelOpen {
		t.Fatalf("theater: full screen %v, shown %v, panel open %v", a.fullscreen, a.theaterShown, a.panelOpen)
	}
	if !tt.HasText("退出影院") || !tt.HasText("选集") {
		t.Fatalf("no theater controls in %q", tt.Texts())
	}
	if tt.HasText("右侧边缘") {
		t.Fatal("the edge sensor is a named control")
	}

	// The pointer near the right edge brings the episodes out, and
	// leaving them puts them away.
	tt.Move(1170, 380)
	tt.Advance(time.Second)
	if !a.panelOpen || !tt.HasText("第 1 集 / 共 130 集") {
		t.Fatalf("the episodes do not show: panel %v, texts %q", a.panelOpen, tt.Texts())
	}
	tt.Move(600, 380)
	tt.Advance(time.Second)
	if a.panelOpen {
		t.Fatal("the episodes stay after the pointer left them")
	}

	tt.Move(1170, 380)
	tt.Advance(time.Second)
	click(t, tt, "收起选集")
	if a.panelOpen || tt.HasText("收起选集") {
		t.Fatal("the episodes stay after the close button")
	}

	// A close by hand holds even with the pointer still at the edge, and
	// leaving clears that hold.
	tt.Move(600, 380)
	tt.Advance(time.Second)
	tt.Move(1170, 380)
	tt.Advance(time.Second)
	if !a.panelOpen {
		t.Fatal("the episodes do not come back after the pointer left")
	}
	tt.ClickAt(20, 300) // the dimmed backdrop
	tt.Advance(time.Second)
	if a.panelOpen {
		t.Fatal("the episodes stay after a click outside them")
	}

	click(t, tt, "退出影院")
	if a.fullscreen || a.theaterShown || a.panelOpen {
		t.Fatalf("out of theater: full screen %v, shown %v, panel open %v", a.fullscreen, a.theaterShown, a.panelOpen)
	}
	if !tt.HasText("影院") || !tt.HasText("第 1 集 / 共 130 集") {
		t.Fatalf("the side panel did not come back: %q", tt.Texts())
	}

	click(t, tt, "退出播放")
	if got := a.router.Path(); got != "/discover" {
		t.Fatalf("at %q after leaving the player", got)
	}
}

// Escape closes the episodes first, then leaves the theater, and only then
// the player.
func TestPlayerEscape(t *testing.T) {
	a, tt := playerApp(t)
	click(t, tt, "影院")
	tt.Move(1170, 380)
	tt.Advance(time.Second)
	if !a.panelOpen {
		t.Fatal("the pointer at the right edge did not show the episodes")
	}

	tt.Key(0, ui.KeyEscape)
	tt.Advance(time.Second)
	if a.panelOpen {
		t.Fatal("Escape did not close the episodes")
	}
	if !a.fullscreen {
		t.Fatal("Escape left the theater with the episodes")
	}
	if got := a.router.Path(); got != "/play/1" {
		t.Fatalf("at %q after closing the episodes", got)
	}

	tt.Key(0, ui.KeyEscape)
	tt.Advance(time.Second)
	if a.fullscreen {
		t.Fatal("Escape did not leave the theater")
	}
	if got := a.router.Path(); got != "/play/1" {
		t.Fatalf("at %q after leaving the theater", got)
	}

	tt.Key(0, ui.KeyEscape)
	tt.Advance(time.Second)
	if got := a.router.Path(); got != "/discover" {
		t.Fatalf("at %q after leaving the player", got)
	}
}

// The page's double click asks for the theater; the window follows.
func TestPlayerDoubleClickTheater(t *testing.T) {
	a := testApp(t)
	a.cur = &playback{seriesID: "1", number: 1, gen: 1}
	a.router.Push("/play/1")
	a.onPlayerEvent(url.Values{"type": {"theater"}, "ep": {"e1"}})
	if !a.fullscreen {
		t.Fatal("the double click did not enter the theater")
	}
	a.onPlayerEvent(url.Values{"type": {"theater"}, "ep": {"e1"}})
	if a.fullscreen {
		t.Fatal("the double click did not leave the theater")
	}
}

// Controls belong to the player, not to the episode's page: between two
// episodes the page still names the old one, and its theater click must
// still work.
func TestTheaterEventWithoutTheCurrentEpisode(t *testing.T) {
	a := testApp(t)
	a.cur = &playback{seriesID: "1", number: 2, gen: 1}
	a.onPlayerEvent(url.Values{"type": {"theater"}, "ep": {"e1"}})
	if !a.fullscreen {
		t.Fatal("the theater click from the old episode was dropped")
	}
}

// The player's controls are the app's bottom bar, not the video's native
// ones, which put the small window and full screen buttons over the picture.
func TestPlayerControlsAreTheBottomBar(t *testing.T) {
	for _, want := range []string{`id="bar"`, `id="seek"`, `id="vol"`, `id="pip"`, `id="prev"`, `id="rew"`, `id="fwd"`, `id="next"`, `<video id="v" playsinline`} {
		if !strings.Contains(playerHTML, want) {
			t.Fatalf("the player page has no %s", want)
		}
	}
	if strings.Contains(playerHTML, `<video id="v" controls`) {
		t.Fatal("the player page still uses the native video controls")
	}
	// The transport buttons sit above the progress bar, centered.
	if strings.Index(playerHTML, `id="transport"`) > strings.Index(playerHTML, `id="seek"`) {
		t.Fatal("the transport buttons are not above the progress bar")
	}
	if !strings.Contains(playerHTML, "#transport{display:flex;align-items:center;justify-content:center;gap:10px;height:36px}") {
		t.Fatal("the transport buttons are not centered")
	}
	// The bar and its progress line are centered and shorter than the
	// picture, and translucent enough to keep the picture visible.
	for _, want := range []string{
		"#bar{position:fixed;left:50%",
		"width:min(680px,calc(100% - 24px));box-sizing:border-box",
		"background:rgba(24,24,26,.42)",
		"#seek{flex:0 1 320px",
		"const hideAfter = 2000;",
	} {
		if !strings.Contains(playerHTML, want) {
			t.Fatalf("the player bar no longer has %q", want)
		}
	}
}

// In the theater the top buttons follow the bottom bar: both hide while
// the picture plays, and both come back when the pointer moves.
func TestTheaterTopButtonsFollowTheBar(t *testing.T) {
	a, tt := playerApp(t)
	click(t, tt, "影院")
	if !tt.HasText("退出播放") || !tt.HasText("退出影院") {
		t.Fatalf("no top buttons at first: %q", tt.Texts())
	}

	a.onPlayerEvent(url.Values{"type": {"chrome"}, "on": {"0"}, "ep": {"e1"}})
	tt.Frame()
	if tt.HasText("退出播放") || tt.HasText("退出影院") {
		t.Fatalf("top buttons stay when the bar hides: %q", tt.Texts())
	}

	a.onPlayerEvent(url.Values{"type": {"chrome"}, "on": {"1"}, "ep": {"e1"}})
	tt.Frame()
	if !tt.HasText("退出播放") || !tt.HasText("退出影院") {
		t.Fatalf("top buttons do not come back with the bar: %q", tt.Texts())
	}
}

// The page cannot see the pointer over the top buttons, so they stay while
// it rests on them, and go once it leaves them.
func TestTheaterTopButtonsStayUnderThePointer(t *testing.T) {
	a, tt := playerApp(t)
	click(t, tt, "影院")
	r, ok := tt.Find("退出影院")
	if !ok {
		t.Fatalf("no 退出影院: %q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()

	a.onPlayerEvent(url.Values{"type": {"chrome"}, "on": {"0"}, "ep": {"e1"}})
	tt.Frame()
	if !tt.HasText("退出播放") || !tt.HasText("退出影院") {
		t.Fatalf("the top buttons hide under the pointer: %q", tt.Texts())
	}

	tt.Move(r.X+r.W/2, r.Y+300)
	tt.Frame()
	if tt.HasText("退出播放") || tt.HasText("退出影院") {
		t.Fatalf("the top buttons stay after the pointer leaves them: %q", tt.Texts())
	}
}

// The player's exit and the series' back stay clear of the traffic lights.
func TestTitleBarButtonsClearOfTrafficLights(t *testing.T) {
	a := testApp(t)
	a.router.Push("/series/1")
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)
	if r, ok := tt.Find("返回"); !ok || r.X != 239 {
		t.Fatalf("the series' back button at %v, not at the content's margin", r)
	}

	a.cur = &playback{seriesID: "1", number: 1, gen: 1}
	a.router.Push("/play/1")
	tt.Advance(time.Second)
	if r, ok := tt.Find("退出播放"); !ok || r.X != 90 {
		t.Fatalf("the player's exit button at %v, not past the traffic lights", r)
	}
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight})
	tt.Advance(time.Second)
	if r, ok := tt.Find("退出播放"); !ok || r.X != 16 {
		t.Fatalf("the player's exit button at %v in full screen, not at 16", r)
	}
}

// The theater's episodes come from the right edge, over the video.
func TestTheaterDrawerOnTheRight(t *testing.T) {
	_, tt := playerApp(t)
	click(t, tt, "影院")
	tt.Move(1170, 380)
	tt.Advance(time.Second)
	r, ok := tt.Find("第 1 集 / 共 130 集")
	if !ok || r.X < 880 {
		t.Fatalf("the episodes at %v, not on the right", r)
	}
	if c, ok := tt.Find("收起选集"); !ok || c.X < 1100 {
		t.Fatalf("the close button at %v, not in the drawer", c)
	}
}

// A guide on the player must not swallow the app's search shortcut on the
// pages before it.
func TestSearchShortcutOutsideThePlayerTour(t *testing.T) {
	a := testApp(t)
	a.playerTutorialOn = true
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)

	tt.Key(ui.Cmd, ui.KeyF)
	tt.Type("abc")
	tt.Advance(time.Second)
	if a.query != "abc" {
		t.Fatalf("search query %q after Cmd-F", a.query)
	}
}

// Full screen opened before the player stays after it closes; only the
// full screen the theater entered is the theater's to leave.
func TestLeavingPlayerKeepsUserFullScreen(t *testing.T) {
	a := testApp(t)
	a.fullscreen = true
	a.cur = &playback{seriesID: "1", number: 1, gen: 1}
	a.router.Push("/play/1")
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)

	click(t, tt, "退出播放")
	if !a.fullscreen {
		t.Fatal("leaving the player left the viewer's full screen")
	}
	if got := a.router.Path(); got != "/discover" {
		t.Fatalf("at %q after leaving the player", got)
	}
}

// The theater's full screen, on the other hand, leaves with the player.
func TestLeavingPlayerLeavesTheTheaterFullScreen(t *testing.T) {
	a, tt := playerApp(t)
	click(t, tt, "影院")
	click(t, tt, "退出播放")
	if a.fullscreen {
		t.Fatal("the theater's full screen stayed after leaving the player")
	}
}

// The drawer can still be opened when an episode failed and its message
// layer covers the video, and the toolbar names the same action.
func TestTheaterEpisodesWithFailedEpisode(t *testing.T) {
	a, tt := playerApp(t)
	click(t, tt, "影院")
	a.cur.state, a.cur.msg = playFailed, "视频加载失败，请重试"
	tt.Advance(time.Second)

	tt.Move(1170, 380)
	tt.Advance(time.Second)
	if !a.panelOpen {
		t.Fatal("the failed episode keeps the drawer closed")
	}
	tt.Move(600, 380)
	tt.Advance(time.Second)
	if a.panelOpen {
		t.Fatal("the drawer stays after the pointer left it")
	}
	if tt.HasText("收起选集") {
		t.Fatalf("the drawer overlay is still there: %q", tt.Texts())
	}
	if !tt.HasText("选集") {
		t.Fatalf("the theater toolbar has no episodes button: %q", tt.Texts())
	}
	if err := tt.Click("选集"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if !a.panelOpen {
		t.Fatal("the episodes button did not open the drawer")
	}
}

// A series too long for one select still changes parts inside the drawer;
// the choices stay in the panel, so it does not close under the pointer.
func TestTheaterPartPickerStaysInDrawer(t *testing.T) {
	a := testApp(t)
	sp := a.series["1"]
	sp.episodes = nil
	for i := 1; i <= 700; i++ {
		sp.episodes = append(sp.episodes, model.Episode{ID: "e" + itoa(i), SeriesID: "1", Number: i, Playable: true})
	}
	sp.series.EpisodeCount = 700
	a.cur = &playback{seriesID: "1", number: 1, gen: 1}
	a.router.Push("/play/1")
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)
	click(t, tt, "影院")
	tt.Move(1170, 380)
	tt.Advance(time.Second)
	if !a.panelOpen {
		t.Fatal("the drawer is not open")
	}
	for _, want := range []string{"1–100", "601–700"} {
		if !tt.HasText(want) {
			t.Fatalf("no part %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("601–700"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if !a.panelOpen || sp.part != 6 {
		t.Fatalf("part %d, drawer open %v after choosing 601–700", sp.part, a.panelOpen)
	}
}

// Opening the drawer brings the episode playing into view, even when the
// side panel already remembered it.
func TestDrawerRevealsCurrentEpisode(t *testing.T) {
	a := testApp(t)
	a.cur = &playback{seriesID: "1", number: 87, gen: 1}
	a.router.Push("/play/1")
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)
	click(t, tt, "影院")
	tt.Move(1170, 380)
	tt.Advance(time.Second)
	if !a.panelOpen {
		t.Fatal("the drawer is not open")
	}
	r, ok := tt.Find("第 87 集")
	if !ok || r.Y < 0 || r.Y > 760 || r.X < 880 {
		t.Fatalf("episode 87 at %v, not in the drawer", r)
	}
}

// The progress line uses each chip's track, not the player's fixed width.
func TestEpisodeProgressWidth(t *testing.T) {
	for _, tc := range []struct {
		width    float32
		progress float64
		want     float32
	}{
		{56, 0.5, 21},
		{44, 0.5, 15},
		{56, 0, 0},
		{14, 0.5, 0},
	} {
		if got := episodeProgressWidth(tc.width, tc.progress); got != tc.want {
			t.Fatalf("width %v, progress %v: got %v, want %v", tc.width, tc.progress, got, tc.want)
		}
	}
}

package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The first launch's tour shows four pages, moves on, and leaves a marker
// so that it doesn't come back.
func TestTutorial(t *testing.T) {
	a := testApp(t)
	a.tutorialOn = true
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)

	if !tt.HasText("新手教程") || !tt.HasText("找剧") {
		t.Fatalf("the tour does not show: %q", tt.Texts())
	}
	for _, want := range []string{"收藏和续播", "双击进出影院", "手势和选集"} {
		click(t, tt, "下一步")
		if !tt.HasText(want) {
			t.Fatalf("no %q after the next page: %q", want, tt.Texts())
		}
	}
	click(t, tt, "开始使用")
	if a.tutorialOn || tt.HasText("新手教程") {
		t.Fatal("the tour stays after its last page")
	}
	if !tutorialSeen(a.dataDir) {
		t.Fatal("the tour left no marker")
	}
}

// The shortcuts the app names read as the system writes them: ui.Cmd is
// Control on Windows, so search is Ctrl+F there and ⌘F on macOS.
func TestKeyNames(t *testing.T) {
	if k := keyNamesFor("windows"); k.cmd != "Ctrl+" || k.shift != "Shift+" {
		t.Fatalf("Windows writes %+v", k)
	}
	if k := keyNamesFor("darwin"); k.cmd != "⌘" || k.shift != "⇧" {
		t.Fatalf("macOS writes %+v", k)
	}

	a := testApp(t)
	a.tutorialOn = true
	tt := ui.NewTester(a.view, 1180, 760)
	tt.Advance(time.Second)
	if !tt.HasText(keys.cmd + "F 随时搜索") {
		t.Fatalf("the tour names no %sF: %q", keys.cmd, tt.Texts())
	}
	_, tt = playerApp(t)
	if !tt.HasText(keys.shift + "← " + keys.shift + "→ 30 秒") {
		t.Fatalf("the player names no %s←: %q", keys.shift, tt.Texts())
	}
}

// Skipping the tour closes it just as finishing does.
func TestTutorialSkip(t *testing.T) {
	a := testApp(t)
	a.tutorialOn = true
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)
	click(t, tt, "跳过")
	if a.tutorialOn || !tutorialSeen(a.dataDir) {
		t.Fatalf("the tour after skipping: on %v, seen %v", a.tutorialOn, tutorialSeen(a.dataDir))
	}
}

// The player's own tour shows on its first visit, walks its four pages,
// and starts the video when it closes.
func TestPlayerTutorial(t *testing.T) {
	a, tt := playerApp(t)
	a.playerTutorialOn = true
	tt.Frame()
	if !tt.HasText("播放器新手指南") || !tt.HasText("双击进出影院") {
		t.Fatalf("the player tour does not show: %q", tt.Texts())
	}
	for _, want := range []string{"右半边调音量，左半边调亮度", "选集自动出现", "底部播放栏"} {
		click(t, tt, "下一步")
		if !tt.HasText(want) {
			t.Fatalf("no %q after the next page: %q", want, tt.Texts())
		}
	}
	click(t, tt, "开始观看")
	if a.playerTutorialOn || tt.HasText("播放器新手指南") {
		t.Fatal("the player tour stays after its last page")
	}
	if !playerTutorialSeen(a.dataDir) {
		t.Fatal("the player tour left no marker")
	}
}

// Escape skips the player tour instead of leaving the player.
func TestPlayerTutorialEscape(t *testing.T) {
	a, tt := playerApp(t)
	a.playerTutorialOn = true
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Advance(time.Second)
	if a.playerTutorialOn {
		t.Fatal("Escape did not skip the player tour")
	}
	if got := a.router.Path(); got != "/play/1" {
		t.Fatalf("at %q after skipping the player tour", got)
	}
	if !playerTutorialSeen(a.dataDir) {
		t.Fatal("the player tour left no marker after Escape")
	}
}

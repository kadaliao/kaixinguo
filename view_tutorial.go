package main

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

// tutorialSeen and playerTutorialSeen report whether a tour has been
// finished on this Mac. A missing or unreadable marker means it has not:
// the tour is there to be read, not to be skipped because a file could
// not be read.
func tutorialSeen(dir string) bool       { return markerSeen(dir, "tutorial") }
func playerTutorialSeen(dir string) bool { return markerSeen(dir, "player-tutorial") }

func markerSeen(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// writeMarker closes a tour for good, or at least until its marker is
// removed. The marker is tiny; writing it on the main thread costs no
// frame, and failing to write it only shows the tour again next launch.
func writeMarker(dir, name string) {
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, name), []byte("1"), 0o644)
}

// finishTutorial closes the app's first launch tour.
func (a *app) finishTutorial() {
	a.tutorialOn = false
	a.tutorialStep = 0
	writeMarker(a.dataDir, "tutorial")
}

// tutorialNext goes to the next page, or closes the last one.
func (a *app) tutorialNext() {
	if a.tutorialStep < len(tutorialSteps)-1 {
		a.tutorialStep++
		return
	}
	a.finishTutorial()
}

// finishPlayerTutorial closes the player's tour and starts the episode it
// paused. The click on the last page is the gesture that allows play.
func (a *app) finishPlayerTutorial() {
	a.playerTutorialOn = false
	a.playerTutorialStep = 0
	a.playerTutorialHeld = false
	a.evalJS("kx.setHold(false)")
	writeMarker(a.dataDir, "player-tutorial")
}

// playerTutorialNext goes to the next page, or closes the last one.
func (a *app) playerTutorialNext() {
	if a.playerTutorialStep < len(playerTutorialSteps)-1 {
		a.playerTutorialStep++
		return
	}
	a.finishPlayerTutorial()
}

type tutorialStep struct {
	title string
	body  string
}

// tutorialSteps is the first launch's tour: the few things the rest of the
// app does not make obvious by itself.
var tutorialSteps = []tutorialStep{
	{
		title: "找剧",
		body:  "推荐、排行榜和新剧都在侧栏，⌘F 随时搜索。",
	},
	{
		title: "收藏和续播",
		body:  "剧集页点「收藏」，或在卡片上右键；看过的剧会出现在「最近播放」，点一下接着看。",
	},
	{
		title: "双击进出影院",
		body:  "播放时双击视频，就能在全屏影院和窗口播放之间切换；也可以点右上角的「影院 / 退出影院」。",
	},
	{
		title: "手势和选集",
		body:  "鼠标滚轮或触控板在画面右半边上下滑动调音量，左半边调亮度；鼠标移到窗口右边缘，选集自动出现，移开自动收起。按 Esc 会先收起选集，再退出影院和播放。",
	},
}

// playerTutorialSteps is the player's own first visit: the controls that
// are not obvious until one has used them.
var playerTutorialSteps = []tutorialStep{
	{
		title: "双击进出影院",
		body:  "播放时双击视频，进入或退出全屏影院；也可以点右上角的「影院 / 退出影院」。",
	},
	{
		title: "右半边调音量，左半边调亮度",
		body:  "鼠标滚轮或触控板在画面右半边上下滑动调音量，左半边调亮度。底部播放栏也有音量滑条。",
	},
	{
		title: "选集自动出现",
		body:  "影院模式下，鼠标靠近窗口右边框，选集自动从右侧出现；鼠标离开选集区域，自动收起。",
	},
	{
		title: "底部播放栏",
		body:  "播放/暂停、快退快进、上一集/下一集、进度、音量和小窗都在画面底部；播放时自动隐藏，移动鼠标出现。影院里顶部按钮也会一起隐藏和出现。",
	},
}

// tutorial shows the app's first launch tour.
func (a *app) tutorial(c *ui.Context) {
	a.tour(c, "新手教程", tutorialSteps, a.tutorialStep, "开始使用",
		func() {
			if a.tutorialStep > 0 {
				a.tutorialStep--
			}
		},
		a.tutorialNext, a.finishTutorial)
}

// playerTutorial shows the player's first visit tour over the paused
// video.
func (a *app) playerTutorial(c *ui.Context) {
	a.tour(c, "播放器新手指南", playerTutorialSteps, a.playerTutorialStep, "开始观看",
		func() {
			if a.playerTutorialStep > 0 {
				a.playerTutorialStep--
			}
		},
		a.playerTutorialNext, a.finishPlayerTutorial)
}

// tour is a small card over the app in a few pages. prev may be nil on
// the first page; skip closes the tour from anywhere.
func (a *app) tour(c *ui.Context, title string, steps []tutorialStep, step int, done string, prev, next, skip func()) {
	if len(steps) == 0 {
		return
	}
	t := c.Theme()
	current := min(max(step, 0), len(steps)-1)
	s := steps[current]
	ui.Overlay(c, func() {
		back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Background(ui.RGBA(0, 0, 0, 0.58)).Center().Modal()
		back.Children(func() {
			card := ui.Column(c.Key("tutorial")).Width(480).Padding(26, 30, 20, 30).
				Background(t.Surface).Radius(16).
				Shadow(0, 20, 52, 8, ui.RGBA(0, 0, 0, 0.45)).
				Role(ui.RoleDialog).Label(title)
			card.Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, title).FontSize(19).Bold()
					ui.Spacer(c)
					ui.Textf(c, "%d / %d", current+1, len(steps)).FontSize(12).TextColor(t.TextMuted)
				})
				ui.Box(c).Height(16)
				ui.Text(c, s.title).FontSize(17).Bold()
				ui.Box(c).Height(6)
				ui.Text(c, s.body).FontSize(13).LineHeight(1.65).MaxWidth(420).TextColor(t.TextMuted)
				ui.Box(c).Height(18)
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					if ui.Button(c.Key("tutorial-skip"), "跳过").Clicked() {
						skip()
					}
					ui.Spacer(c)
					if current > 0 && prev != nil {
						if ui.Button(c.Key("tutorial-prev"), "上一步").Clicked() {
							prev()
						}
					}
					label := "下一步"
					if current == len(steps)-1 {
						label = done
					}
					if a.tourNextButton(c, label) {
						next()
					}
				})
			})
			// Escape skips the tour, as the button does.
			if back.OverlayShortcut(0, ui.KeyEscape) {
				skip()
			}
		})
	})
}

// tourNextButton is the tour's primary button, the one that moves on.
func (a *app) tourNextButton(c *ui.Context, label string) bool {
	t := c.Theme()
	b := ui.ButtonBase(c.Key("tutorial-next")).Padding(8, 18).Radius(8).Label(label).Cursor(ui.CursorPointer)
	bg := t.Accent
	if b.Pressed() {
		bg = t.AccentPressed
	} else if b.Hovered() {
		bg = t.AccentHover
	}
	b.Background(bg).TextColor(t.AccentText)
	b.Children(func() {
		ui.Text(c, label).FontSize(13).FontWeight(600)
	})
	return b.Clicked()
}

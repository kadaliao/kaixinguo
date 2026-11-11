package main

import (
	"fmt"

	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

var (
	black            = ui.RGB(0, 0, 0)
	playerPanelColor = ui.RGB(22, 22, 24)
)

const playerPanelWidth = 296

// playerView is the episode playing, beside the episodes, or in full
// screen with the episodes a drawer of the video.
func (a *app) playerView(c *ui.Context, id string) {
	sp := a.seriesPage(id)
	if a.cur == nil || a.cur.seriesID != id {
		// Reached through history: go on where the viewer was.
		if sp.series != nil && len(sp.episodes) > 0 {
			sid, n, start, _ := a.resumePoint(sp)
			a.play(sid, n, start)
		}
	}
	if !a.playerSet && a.web != nil {
		a.playerSet = true
		a.evalJS(fmt.Sprintf("kx.setDoubleClickInterval(%d)", doubleClickInterval().Milliseconds()))
	}
	// Theater is the player filling the screen. The episodes come back
	// from the right edge, hidden until asked for.
	theater := a.onPlayer && a.fullscreen
	if theater != a.theaterShown {
		a.theaterShown = theater
		a.panelOpen = false
		a.panelSuppressed = false
		a.panelJustClosed = false
		a.panelReveal = false
	}
	// The player's first visit holds playback for its own tour; the
	// last page lets the episode start.
	if a.playerTutorialOn && !a.playerTutorialHeld {
		a.playerTutorialHeld = true
		a.evalJS("kx.setHold(true)")
	}
	dark := *ui.DarkTheme()
	dark.Accent, dark.AccentText = c.Theme().Accent, c.Theme().AccentText
	ui.Themed(c, &dark).Fill().Background(black).Children(func() {
		var edgeHovered bool
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).Background(black).Children(func() {
				if a.web != nil {
					ui.WebView(c, a.web).Fill()
				}
				a.playerOverlay(c)
				// The edge is the theater's way into the episodes: the
				// pointer near it opens them, and leaving them closes them.
				// It goes over the failure layer, which would otherwise
				// keep the drawer out of reach.
				if theater {
					edgeHovered = a.episodesEdge(c)
				}
				ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(titleBarHeight).DragWindow()
				// In the theater the top buttons keep the bottom bar company:
				// both hide while the picture plays and come back on a move.
				// The page cannot see the pointer over them, so they also
				// stay while it rests there.
				hovered := false
				if !theater || a.playerChrome || a.playerTopHovered {
					exit := a.exitButton(c)
					tools := a.playerToolbar(c, theater)
					hovered = exit || tools
				}
				a.playerTopHovered = hovered
			})
			if !theater {
				a.playerPanel(c, sp)
			}
		})
		if theater {
			justClosed := a.panelJustClosed
			a.panelJustClosed = false
			if edgeHovered && !a.panelSuppressed {
				if !a.panelOpen {
					a.panelReveal = true
				}
				a.panelOpen = true
			}
			if a.panelOpen {
				justOpened := a.panelReveal
				hovered := a.theaterPanel(c, sp)
				if hovered {
					a.panelPinned = false
				}
				if !justOpened && !a.panelPinned && !edgeHovered && !hovered {
					a.panelOpen = false
					a.panelReveal = false
					// The overlay was already built this frame; take it away
					// on the next one.
					c.Invalidate()
				}
			} else if !edgeHovered && !justClosed {
				// Let the edge open the episodes again once the pointer
				// has left the place a close came from. The frame after
				// a close still has the closing overlay over the edge, so
				// it does not count as having left.
				a.panelSuppressed = false
			}
		}
		if a.playerTutorialOn {
			a.playerTutorial(c)
		}
	})
}

// playerShortcuts are the player's keys. The window registers them, not
// the player's page, so they keep working when the focus leaves the page,
// as it does when the theater's episodes close.
func (a *app) playerShortcuts(c *ui.Context) {
	c.OnShortcut(0, ui.KeySpace, func() { a.evalJS("kx.toggle()") })
	c.OnShortcut(0, ui.KeyLeft, func() { a.evalJS("kx.seek(-5)") })
	c.OnShortcut(0, ui.KeyRight, func() { a.evalJS("kx.seek(5)") })
	c.OnShortcut(0, ui.KeyDown, func() { a.step(1) })
	c.OnShortcut(0, ui.KeyUp, func() { a.step(-1) })
	c.OnShortcut(0, ui.KeyEscape, a.playerEscape)
}

// playerEscape closes the episodes first, then leaves the theater, and
// only then the player.
func (a *app) playerEscape() {
	switch {
	case a.panelOpen:
		a.closePanel()
	case a.theaterShown:
		a.exitTheater()
	default:
		a.leavePlayer()
	}
}

// toggleTheater enters or leaves the full-screen theater.
func (a *app) toggleTheater() {
	a.panelOpen = false
	a.panelSuppressed = false
	a.panelJustClosed = false
	a.panelReveal = false
	a.panelPinned = false
	if a.fullscreen {
		a.theaterFullscreen = false
		a.setFullScreen(false)
		return
	}
	a.theaterFullscreen = true
	a.setFullScreen(true)
}

// exitTheater leaves full screen, back to the player beside its episodes.
func (a *app) exitTheater() {
	a.panelOpen = false
	a.panelSuppressed = false
	a.panelJustClosed = false
	a.panelReveal = false
	a.panelPinned = false
	a.theaterShown = false
	a.theaterFullscreen = false
	if a.fullscreen {
		a.setFullScreen(false)
	}
}

// leavePlayer leaves the player, and the full screen if the theater
// entered it. A full screen the viewer opened before stays.
func (a *app) leavePlayer() {
	a.panelOpen = false
	a.panelSuppressed = false
	a.panelJustClosed = false
	a.panelReveal = false
	a.panelPinned = false
	a.theaterShown = false
	owned := a.theaterFullscreen
	a.theaterFullscreen = false
	if a.fullscreen && owned {
		a.setFullScreen(false)
	}
	if a.router.CanGoBack() {
		a.router.Back()
		return
	}
	if a.cur != nil {
		a.router.Replace("/series/" + a.cur.seriesID)
		return
	}
	a.router.Replace("/discover")
}

// setFullScreen remembers the window's state as it asks for it, so the
// theater shows before the system finishes the animation.
func (a *app) setFullScreen(v bool) {
	a.fullscreen = v
	if a.win != nil {
		a.win.SetFullScreen(v)
	}
}

// exitButton leaves the player from the video's top left, clear of the
// window's traffic lights. It reports whether the pointer is over it.
func (a *app) exitButton(c *ui.Context) bool {
	b := ui.ButtonBase(c.Key("exit")).Absolute().Top((titleBarHeight-30)/2).Left(titleBarLeft(c, 10)).
		Size(30, 30).Radius(15).Center().Material(glass.Glass{Interactive: true}).
		Label("退出播放").Tooltip("退出播放（Esc）").Cursor(ui.CursorPointer)
	b.Children(func() { ui.Icon(c, iconClose).Size(14, 14) })
	if b.Clicked() {
		a.leavePlayer()
	}
	return b.Hovered()
}

// playerToolbar holds the theater's controls: the episodes drawer and
// the way in or out of full screen. It stays out of the window controls'
// way, at the video's top right. It reports whether the pointer is over
// it.
func (a *app) playerToolbar(c *ui.Context, theater bool) bool {
	bar := ui.Row(c).Absolute().Top(10).Right(14).Gap(2).Padding(3).Radius(12).
		Material(glass.Glass{})
	bar.Children(func() {
		if theater {
			if a.playerToolButton(c, "episodes", iconList, "选集", a.panelOpen) {
				if a.panelOpen {
					a.closePanel()
				} else {
					a.panelOpen = true
					a.panelSuppressed = false
					a.panelReveal = true
					a.panelPinned = true
				}
			}
		}
		label := "影院"
		if theater {
			label = "退出影院"
		}
		if a.playerToolButton(c, "theater", iconTheater, label, theater) {
			a.toggleTheater()
		}
	})
	return bar.Hovered()
}

// playerToolButton is a control of the player's toolbar; on lights it as
// the current mode. It reports a click.
func (a *app) playerToolButton(c *ui.Context, key string, ic *ui.SVG, label string, on bool) bool {
	b := ui.ButtonBase(c.Key(key)).Padding(6, 10).Radius(9).Label(label).Tooltip(label).Cursor(ui.CursorPointer)
	switch {
	case b.Pressed():
		b.Background(ui.RGBA(255, 255, 255, 0.2))
	case b.Hovered():
		b.Background(ui.RGBA(255, 255, 255, 0.13))
	case on:
		b.Background(ui.RGBA(255, 255, 255, 0.1))
	}
	b.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, ic).Size(14, 14).TextColor(ui.RGBA(255, 255, 255, 0.9))
			ui.Text(c, label).FontSize(12).FontWeight(500).SingleLine().TextColor(ui.RGBA(255, 255, 255, 0.92))
		})
	})
	return b.Clicked()
}

// episodesEdge is the invisible strip just inside the right edge. Its
// hover is what opens the theater's episodes. It is thin enough to sit in
// the padding beside the toolbar and bottom bar, whose controls are above
// it and keep the pointer.
func (a *app) episodesEdge(c *ui.Context) bool {
	b := ui.Box(c).Absolute().Right(0).Top(0).Bottom(0).Width(16).Cursor(ui.CursorPointer)
	return b.Hovered()
}

// closePanel puts the theater's episodes away by hand. The edge does not
// bring them back until the pointer has left it.
func (a *app) closePanel() {
	a.panelOpen = false
	a.panelSuppressed = true
	a.panelJustClosed = true
	a.panelReveal = false
	a.panelPinned = false
}

// theaterPanel is the episodes drawer over the video: a dimmed backdrop
// closes it, as Escape and the close button do. It reports whether the
// pointer is inside the panel, which keeps it open.
func (a *app) theaterPanel(c *ui.Context, sp *seriesPage) bool {
	var hovered bool
	ui.Overlay(c, func() {
		back := ui.Row(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Justify(ui.End).
			Background(ui.RGBA(0, 0, 0, 0.42))
		back.Children(func() {
			panel := ui.Column(c.Key("theater-panel")).Width(playerPanelWidth).FillHeight().
				Background(playerPanelColor).Shadow(-18, 0, 48, 0, ui.RGBA(0, 0, 0, 0.5)).
				Role(ui.RoleDialog).Label("选集")
			panel.Children(func() {
				ui.Row(c).Height(titleBarHeight).Shrink(0).Padding(0, 12, 0, 18).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "选集").FontSize(17).Bold()
					ui.Spacer(c)
					if a.closePanelButton(c) {
						a.closePanel()
					}
				})
				ui.Divider(c).Background(ui.RGBA(255, 255, 255, 0.06))
				reveal := a.panelReveal
				a.panelReveal = false
				a.playerPanelBody(c, sp, true, reveal)
			})
			hovered = panel.Hovered()
			if panel.PressedOutside() || back.OverlayShortcut(0, ui.KeyEscape) {
				a.closePanel()
			}
		})
	})
	return hovered
}

// closePanelButton puts the theater's episodes away. It reports a click.
func (a *app) closePanelButton(c *ui.Context) bool {
	b := ui.ButtonBase(c.Key("panel-close")).Size(26, 26).Radius(13).Center().
		Label("收起选集").Tooltip("收起选集").Cursor(ui.CursorPointer)
	switch {
	case b.Pressed():
		b.Background(ui.RGBA(255, 255, 255, 0.2))
	case b.Hovered():
		b.Background(ui.RGBA(255, 255, 255, 0.14))
	default:
		b.Background(ui.RGBA(255, 255, 255, 0.08))
	}
	b.Children(func() { ui.Icon(c, iconClose).Size(13, 13) })
	return b.Clicked()
}

// playerOverlay is what covers the video while it isn't playing: a
// spinner while it loads, or what went wrong.
func (a *app) playerOverlay(c *ui.Context) {
	p := a.cur
	if p == nil || p.state == playPlaying {
		return
	}
	t := c.Theme()
	// Below the title bar, so the exit and the episodes stay reachable
	// when an episode fails.
	ov := ui.Column(c).Absolute().Top(titleBarHeight).Left(0).Right(0).Bottom(0).Center().Gap(14)
	if p.state == playFailed {
		ov.Background(ui.RGBA(0, 0, 0, 0.82))
	} else {
		ov.PassThrough()
		if p.state == playResolving {
			ov.Background(ui.RGBA(0, 0, 0, 0.5))
		}
	}
	ov.Children(func() {
		if p.state != playFailed {
			ui.Spinner(c).FontSize(26).TextColor(ui.RGBA(255, 255, 255, 0.85)).Label("正在加载")
			return
		}
		ui.Text(c, p.msg).FontSize(15).TextColor(t.Text).TextAlign(ui.Center).MaxWidth(420)
		ui.Row(c).Gap(10).Children(func() {
			if ui.Button(c.Key("retry"), "重试").Clicked() {
				a.play(p.seriesID, p.number, p.start)
			}
			if sp := a.series[p.seriesID]; sp != nil {
				if _, ok := sp.episode(p.number + 1); ok {
					if ui.Button(c.Key("next"), "下一集").Clicked() {
						a.step(1)
					}
				}
			}
		})
	})
}

// playerPanel is the series' title and its episodes, beside the video.
func (a *app) playerPanel(c *ui.Context, sp *seriesPage) {
	ui.Column(c).Width(playerPanelWidth).Shrink(0).Background(playerPanelColor).Children(func() {
		ui.Box(c).Height(titleBarHeight).Shrink(0).DragWindow()
		a.playerPanelBody(c, sp, false, false)
	})
}

// playerPanelBody is what the episode list shows, under its drag area or
// the drawer's title.
func (a *app) playerPanelBody(c *ui.Context, sp *seriesPage, drawer, forceReveal bool) {
	t := c.Theme()
	if sp.series == nil {
		ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).FontSize(18) })
		return
	}
	cur := 0
	if a.cur != nil && a.cur.seriesID == sp.id {
		cur = a.cur.number
	}
	ui.Column(c).Padding(0, 22, 16, 22).Gap(6).Children(func() {
		ui.Text(c, sp.series.Title).FontSize(17).Bold().MaxLines(2)
		ui.Textf(c, "第 %d 集 / 共 %d 集", cur, len(sp.episodes)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
		if len(sp.seasons) > 1 {
			ui.Box(c).Height(4)
			a.seasonPicker(c, sp, func(se model.Season) { a.playSeason(se.SeriesID) })
		}
		a.partPicker(c, sp, drawer)
	})
	ui.Divider(c).Background(ui.RGBA(255, 255, 255, 0.06))
	// Bring the episode playing into view as it changes.
	reveal := cur > 0 && (forceReveal || cur != sp.lastShown)
	if reveal {
		sp.part = (cur - 1) / episodesPerPart
		sp.lastShown = cur
	}
	lo, hi := a.partRange(sp)
	w, _ := a.hist.get(sp.id)
	ui.Scroll(c).Grow(1).Padding(14, 16, 22, 16).Children(func() {
		ui.Row(c).Wrap().Gap(8).Children(func() {
			for _, e := range sp.episodes[lo:hi] {
				watched, progress := w.episodeState(e.Number)
				chip := a.episodeChip(c, e, e.Number == cur, watched, progress, 44, t)
				if reveal && e.Number == cur {
					chip.ScrollIntoView()
				}
				if chip.Clicked() && e.Playable && e.Number != cur {
					a.play(sp.id, e.Number, w.episodeStart(e.Number))
				}
			}
		})
	})
	ui.Text(c, "空格 暂停　← → 快进快退　↑ ↓ 换集").FontSize(11).TextColor(t.TextMuted.Alpha(0.8)).
		Padding(10, 22, 16, 22).SingleLine()
}

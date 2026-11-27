package main

import (
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
)

// updaterOptions has the update window speak Chinese, as the app does,
// whatever the system's language. The window's own Chinese puts spaces
// around the app's name, which these texts leave out.
var updaterOptions = updater.Options{
	Language: "zh-Hans",
	Strings: map[string]updater.Strings{"zh-Hans": {
		Available:          "%[1]s有新版本了！",
		UnavailableMessage: "%[1]s无法在当前位置自行更新。如果是直接从安装镜像里打开的，请先把它拖进「应用程序」文件夹再打开。",
		ReadyMessage:       "%[1]s %[2]s 已经装好，下次打开时就是新版本。现在重新启动就能用上。",
	}},
}

// useUpdater has the app look for a newer release on GitHub 10 seconds
// after launch and then once a day, and offer it in a window: install it,
// remind later, or skip it. The app's menu checks on demand. Builds without
// the update key in mygo.json, as mygo dev's, never check.
func useUpdater(a *app) {
	mygo.Use(native.New(updaterOptions))
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		// The default app menu, with 检查更新… after About and 设置… after it.
		{Label: mygo.App.Name(), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			updater.MenuItem(),
			mygo.Separator(),
			{Label: "设置…", Accelerator: "CmdOrCtrl+,", Click: func(_ *mygo.MenuItem, win *mygo.Window) {
				if a.win != nil {
					a.win.Update(a.openSettings)
				}
			}},
			macOnly(mygo.Separator()),
			{Role: mygo.RoleServices},
			macOnly(mygo.Separator()),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleFileMenu},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	}))
}

// macOnly hides an item outside macOS. MyGo hides the items of macOS's own
// there, such as Services and Hide, but not the separators around them,
// which would stack up in the menu Alt opens on Windows.
func macOnly(it *mygo.MenuItem) *mygo.MenuItem {
	it.Hidden = runtime.GOOS != "darwin"
	return it
}

// watchUpdates keeps the version the last check found, which the sidebar
// offers until it is installed or skipped. Call it once the window exists.
func (a *app) watchUpdates() {
	a.checkUpdates = updater.CheckForUpdates
	updater.OnChange(func() {
		v := updater.AvailableVersion()
		a.win.Update(func() { a.newVersion = v })
	})
}

// updateBanner is the sidebar's note of a newer version, at its foot; a
// click shows the update window again.
func (a *app) updateBanner(c *ui.Context) {
	if a.newVersion == "" || a.checkUpdates == nil {
		return
	}
	t := c.Theme()
	b := ui.ButtonBase(c.Key("update")).Margin(4, 12, 12, 12).Padding(8, 10).Radius(8).
		Label("新版本 " + a.newVersion).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(t.Accent.Alpha(0.16))
	} else {
		b.Background(t.Accent.Alpha(0.1))
	}
	b.Children(func() {
		ui.Row(c).Grow(1).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconUpdate).Size(14, 14).Shrink(0).TextColor(t.Accent)
			ui.Column(c).Gap(1).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, "新版本 "+a.newVersion).FontSize(12).FontWeight(600).SingleLine()
				ui.Text(c, "点这里更新").FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
		})
	})
	if b.Clicked() {
		a.checkUpdates()
	}
}

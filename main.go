// 开心果 is a small, fast desktop player for Hongguo short dramas, built on
// the openhongguo SDK and MyGo's native UI.
package main

import (
	"log"
	"net/url"
	"path/filepath"

	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	titleBarHeight = 40
	sidebarWidth   = 220
)

// app is the state the window shows. Everything in it belongs to the main
// thread: work on other goroutines hands its results over with update.
type app struct {
	win    *mygo.Window
	ready  chan struct{} // closed once win is set
	router *ui.Router
	client *sdk.Client
	covers *covers
	hist   *history
	favs   *favorites
	proxy  *proxy

	cacheDir string
	dataDir  string

	// The first launch's tour, until it is finished or skipped.
	tutorialOn   bool
	tutorialStep int

	// The sidebar.
	query        string
	searchField  ui.Handle
	section      string // the sidebar's page the page shown belongs to
	recentOpen   bool
	recentFilter string // recent list filter: "", want, watching or done
	sideShown    float32

	// The catalog.
	home         *feed
	feeds        map[string]*feed
	rankContent  string
	rankBoard    string
	rankIdx      int
	rankContents []model.RankContent
	newDate      string
	newDates     []string
	hot          *model.HotSearch
	hotLoading   bool
	hotErr       error
	favFeed      *feed
	favRev       int // the favorites' rev favFeed was built from

	// Series and playback.
	series  map[string]*seriesPage
	futures map[string]*future
	cur     *playback
	gen     int

	// The theater: the player filling the screen, its episodes a drawer
	// that follows the pointer to the right edge.
	fullscreen        bool
	theaterFullscreen bool // the theater entered full screen, so leaving may leave it
	theaterShown      bool
	panelOpen         bool
	panelSuppressed   bool // closed by hand, until the pointer leaves
	panelJustClosed   bool // the overlay is a frame behind the close
	panelReveal       bool // the next drawer scrolls to the episode playing
	panelPinned       bool // opened from the toolbar; stays until entered or closed

	// The player's first entry shows its own small tour.
	playerTutorialOn   bool
	playerTutorialStep int
	playerTutorialHeld bool

	// The appearance to go back to when the player closes.
	playerTheme mygo.ThemeSource

	// A newer version the updater found, and how to offer it again.
	newVersion   string
	checkUpdates func()

	// The player's web view.
	web       *mygo.WebView
	webReady  bool
	pendingJS []string    // what the page runs once it has loaded
	js        chan string // what the page runs, in order
	onPlayer  bool
	playerSet bool // the player page has its system settings
	// playerChrome is whether the page's bottom bar is showing. In the
	// theater the top buttons hide and come back with it.
	playerChrome bool
	// playerTopHovered is whether the pointer was over the top buttons,
	// which keeps them as the bar hides.
	playerTopHovered bool
}

func newApp(dataDir, cacheDir string) *app {
	a := &app{
		ready:        make(chan struct{}),
		router:       ui.NewRouter("/discover"),
		cacheDir:     cacheDir,
		dataDir:      dataDir,
		section:      "/discover",
		recentOpen:   true,
		sideShown:    1,
		feeds:        map[string]*feed{},
		series:       map[string]*seriesPage{},
		futures:      map[string]*future{},
		hist:         loadHistory(dataDir),
		favs:         loadFavorites(dataDir),
		playerChrome: true,
	}
	a.covers = newCovers(filepath.Join(cacheDir, "covers"), a.update)
	a.home = newFeed(a.discover)
	// Last launch's recommendations show at once; fresh ones replace them.
	if cards := a.loadHomeCache(); len(cards) > 0 {
		a.home.add(page{cards: cards, more: true})
		a.home.stale = true
	}
	a.home.onPage = func(page) { a.saveHomeCache(a.home.cards) }
	return a
}

// update runs fn on the main thread and redraws, once the window exists.
func (a *app) update(fn func()) {
	<-a.ready
	a.win.Update(fn)
}

func main() {
	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	cacheDir, err := mygo.App.Path(mygo.PathCache)
	if err != nil {
		log.Fatal(err)
	}
	a := newApp(dataDir, cacheDir)
	a.tutorialOn = !tutorialSeen(dataDir)
	a.playerTutorialOn = !playerTutorialSeen(dataDir)
	if a.client, err = newClient(dataDir); err != nil {
		log.Fatal(err)
	}
	if a.proxy, err = newProxy(func(q url.Values) { a.update(func() { a.onPlayerEvent(q) }) }); err != nil {
		log.Fatal(err)
	}
	// What was saved last, as the window closed, reaches the disk.
	mygo.App.OnQuit(func() {
		a.hist.store.flush()
		a.favs.store.flush()
	})
	useUpdater()
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "开心果",
			Width:     1180,
			Height:    760,
			MinWidth:  780,
			MinHeight: 520,
			StateKey:  "main",
			// Hidden, not HiddenInset: the inset style adds a toolbar
			// taller than the title bar the app draws.
			TitleBarStyle:        mygo.TitleBarHidden,
			TrafficLightPosition: &mygo.Point{X: 20, Y: 13},
			TitleBarHeight:       titleBarHeight,
			Vibrancy:             mygo.VibrancySidebar,
			Content:              ui.View(a.view),
		})
		a.fullscreen = a.win.IsFullScreen()
		close(a.ready)
		a.watchUpdates()
		// The theater follows the window: entering full screen with the
		// green button does what the theater button does.
		a.win.OnEnterFullScreen(func() {
			a.win.Update(func() { a.fullscreen = true })
		})
		a.win.OnLeaveFullScreen(func() {
			a.win.Update(func() {
				a.fullscreen = false
				a.theaterFullscreen = false
				a.panelOpen = false
				a.panelReveal = false
				a.panelPinned = false
				a.theaterShown = false
			})
		})
		a.loadMore(a.home)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

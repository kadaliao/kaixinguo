package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/beilunyang/openhongguo/adapters"
	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/egoist/mygo/ui"
)

// card is a series as a grid shows it.
type card struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Cover string `json:"cover,omitempty"`
	Sub   string `json:"sub,omitempty"`  // the line under the title
	Rank  int    `json:"rank,omitempty"` // its place on a chart
	// Episodes is how many episodes the series has, when the page that
	// listed it said: Sub may say something else, as a chart's score.
	Episodes int `json:"episodes,omitempty"`
}

// page is what a feed's fetch returns.
type page struct {
	cards  []card
	cursor string
	more   bool
	// What some feeds also tell.
	contents []model.RankContent
	board    string
	dates    []string
}

type fetcher func(ctx context.Context, cursor string) (page, error)

// feed is a list of series loaded a page at a time. It belongs to the
// main thread.
type feed struct {
	fetch     fetcher
	cards     []card
	seen      map[string]bool
	cursor    string
	more      bool
	started   bool
	loading   bool
	stale     bool // the cards came from the disk cache: the first page replaces them
	err       error
	errAt     time.Time
	tries     int // retries made by themselves since the last success
	grid      ui.GridState
	showWatch bool // cards show the viewer's progress, as on the favorites page
	onPage    func(page)
	order     func([]card) // sorts the cards after each page, when set
}

func newFeed(fetch fetcher) *feed {
	return &feed{fetch: fetch, seen: map[string]bool{}, more: true}
}

func (f *feed) add(p page) {
	cards := p.cards
	if f.stale {
		// Fresh series first, then those of the cache, so the grid never
		// shrinks under the viewer.
		cards = append(append([]card(nil), p.cards...), f.cards...)
		f.cards, f.seen, f.stale = nil, map[string]bool{}, false
	}
	added := 0
	for _, k := range cards {
		if k.ID == "" || f.seen[k.ID] {
			continue
		}
		f.seen[k.ID] = true
		f.cards = append(f.cards, k)
		added++
	}
	f.cursor, f.more = p.cursor, p.more
	// Search pages repeat what came before: a page with nothing new ends
	// the feed, or the grid would ask for more forever.
	if added == 0 && len(p.cards) > 0 {
		f.more = false
	}
	if f.order != nil {
		f.order(f.cards)
	}
}

// loadMore fetches the next page, unless one is on its way, the feed has
// no more, or the last try failed a moment ago.
func (a *app) loadMore(f *feed) {
	if f.loading || (f.started && !f.stale && !f.more) {
		return
	}
	if f.err != nil && time.Since(f.errAt) < 5*time.Second {
		return
	}
	f.loading, f.started = true, true
	cursor := f.cursor
	if f.stale {
		cursor = ""
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		p, err := f.fetch(ctx, cursor)
		if err != nil && os.Getenv("KX_DEBUG") != "" {
			log.Printf("feed: %v", err)
		}
		a.update(func() {
			f.loading = false
			if err != nil {
				f.err, f.errAt = err, time.Now()
				return
			}
			f.err, f.tries = nil, 0
			f.add(p)
			if f.onPage != nil {
				f.onPage(p)
			}
		})
	}()
}

// retry fetches again after an error, now.
func (a *app) retry(f *feed) {
	f.err = nil
	a.loadMore(f)
}

func contextTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func newClient(dir string) (*sdk.Client, error) {
	opts := sdk.Options{RetryAttempts: 3, DeviceResets: sdk.Int(3)}
	if os.Getenv("KX_DEBUG") != "" {
		opts.Logger = log.New(os.Stderr, "sdk: ", log.Ltime|log.Lmicroseconds)
	}
	return adapters.NewClient(filepath.Join(dir, "device.json"), opts)
}

// discover reads the recommended channel one server page at a time, so
// each shows as soon as it comes; the grid asks for the next as it nears
// its end.
func (a *app) discover(ctx context.Context, cursor string) (page, error) {
	out := page{cursor: cursor}
	for i := 0; i < 3; i++ { // past pages that bring nothing
		p, err := a.client.Discover(ctx, sdk.DiscoverOptions{Cursor: out.cursor})
		if err != nil {
			return out, err
		}
		for _, s := range p.Items {
			out.cards = append(out.cards, summaryCard(s))
		}
		out.cursor, out.more = p.Cursor, p.HasMore
		if len(out.cards) > 0 || !p.HasMore {
			break
		}
	}
	return out, nil
}

func (a *app) rankFetcher(content, board string) fetcher {
	return func(ctx context.Context, cursor string) (page, error) {
		r, err := a.client.ShortRank(ctx, sdk.ShortRankQuery{Content: content, Board: board, Cursor: cursor})
		if err != nil {
			return page{}, err
		}
		out := page{cursor: r.Cursor, more: r.HasMore, contents: r.Contents, board: r.Board}
		for _, it := range r.Items {
			if it.SeriesID == "" {
				continue // an actor's row
			}
			sub := it.Score
			if sub == "" {
				sub = episodeCount(it.EpisodeCount)
			}
			out.cards = append(out.cards, card{ID: it.SeriesID, Title: it.Title, Cover: it.CoverURL, Sub: sub,
				Rank: it.Position, Episodes: it.EpisodeCount})
		}
		return out, nil
	}
}

func (a *app) newFetcher(date string) fetcher {
	return func(ctx context.Context, cursor string) (page, error) {
		r, err := a.client.NewReleases(ctx, sdk.NewReleaseQuery{Date: date, Cursor: cursor})
		if err != nil {
			return page{}, err
		}
		out := page{cursor: r.Cursor, more: r.HasMore, dates: r.Dates}
		for _, it := range r.Items {
			sub := it.Heat
			if !it.Online && it.Note != "" {
				sub = it.Note
			}
			out.cards = append(out.cards, card{ID: it.SeriesID, Title: it.Title, Cover: it.CoverURL, Sub: sub,
				Episodes: it.EpisodeCount})
		}
		return out, nil
	}
}

// generalCursor marks a search cursor that pages through the 综合 tab.
const generalCursor = "综合:"

func (a *app) searchFetcher(query string) fetcher {
	return func(ctx context.Context, cursor string) (page, error) {
		// The 剧集 tab lists each season of a series; 综合 shows its first
		// season alone, among clips. Some anonymous devices get an empty
		// 剧集 tab, which the SDK reports as ErrAnonymousLimited, so a first
		// page that finds nothing there asks 综合 instead. The cursor
		// carries the tab, keeping the later pages on the first one's.
		tab := "剧集"
		if rest, ok := strings.CutPrefix(cursor, generalCursor); ok {
			tab, cursor = "", rest
		}
		r, err := a.client.Search(ctx, query, sdk.SearchOptions{Tab: tab, Cursor: cursor, PageSize: 20})
		if tab != "" && cursor == "" && err != nil && !errors.Is(err, sdk.ErrEndOfList) {
			tab = ""
			r, err = a.client.Search(ctx, query, sdk.SearchOptions{PageSize: 20})
		}
		if err != nil {
			if errors.Is(err, sdk.ErrAnonymousLimited) || errors.Is(err, sdk.ErrEndOfList) {
				return page{}, nil // nothing found
			}
			return page{}, err
		}
		out := page{cursor: r.Cursor, more: r.HasMore && len(r.Items) > 0}
		if tab == "" && out.cursor != "" {
			out.cursor = generalCursor + out.cursor
		}
		var missing []string
		for _, s := range r.Items {
			out.cards = append(out.cards, summaryCard(s))
			if s.CoverURL == "" {
				missing = append(missing, s.ID)
			}
		}
		// Search results come without covers: read them from the details.
		if len(missing) > 0 {
			if details, err := a.client.GetSeriesBatch(ctx, missing); err == nil {
				byID := map[string]*model.Series{}
				for _, d := range details {
					if d.Series != nil {
						byID[d.Series.ID] = d.Series
					}
				}
				for i, k := range out.cards {
					if s := byID[k.ID]; s != nil {
						out.cards[i].Cover = s.CoverURL
						if s.EpisodeCount > 0 {
							out.cards[i].Sub = episodesLabel(s)
							out.cards[i].Episodes = s.EpisodeCount
						}
					}
				}
			}
		}
		return out, nil
	}
}

func summaryCard(s model.SeriesSummary) card {
	return card{ID: s.ID, Title: s.Title, Cover: s.CoverURL, Sub: episodeCount(s.EpisodeCount), Episodes: s.EpisodeCount}
}

// episodeCount is a card's line for a series of n episodes, or nothing
// when n is unknown.
func episodeCount(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d 集", n)
}

func episodesLabel(s *model.Series) string {
	if s.RightText != "" {
		return s.RightText
	}
	if s.Status == model.SeriesUpdating {
		return fmt.Sprintf("更新至 %d 集", s.EpisodeCount)
	}
	return fmt.Sprintf("全 %d 集", s.EpisodeCount)
}

// heat writes a count as the app does: 4165万, 1.2亿.
func heat(n int64) string {
	switch {
	case n >= 100_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e8), ".0") + "亿"
	case n >= 10_000:
		return fmt.Sprintf("%d万", n/10_000)
	default:
		return fmt.Sprint(n)
	}
}

// seriesPage is a series and its episodes, as its page shows them.
type seriesPage struct {
	id        string
	series    *model.Series
	episodes  []model.Episode
	loading   bool
	err       error
	part      int // which hundred of episodes shows
	lastShown int // the episode the player last brought into view

	// The seasons of the series, the same list on each season's page.
	seasons        []model.Season
	seasonsLoading bool
}

func (a *app) seriesPage(id string) *seriesPage {
	sp := a.seriesEntry(id)
	if sp.series == nil && !sp.loading && sp.err == nil {
		a.loadSeries(sp)
	}
	return sp
}

// seriesEntry returns the series' page without loading it.
func (a *app) seriesEntry(id string) *seriesPage {
	sp := a.series[id]
	if sp == nil {
		sp = &seriesPage{id: id}
		a.series[id] = sp
	}
	return sp
}

func (a *app) loadSeries(sp *seriesPage) {
	sp.loading, sp.err = true, nil
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		type res struct {
			s   *model.Series
			eps []model.Episode
			err error
		}
		sc, ec := make(chan res, 1), make(chan res, 1)
		go func() {
			s, err := a.client.GetSeries(ctx, sp.id)
			sc <- res{s: s, err: err}
		}()
		go func() {
			p, err := a.client.ListEpisodes(ctx, sp.id, sdk.EpisodeOptions{})
			r := res{err: err}
			if p != nil {
				r.eps = p.Items
			}
			ec <- r
		}()
		s, e := <-sc, <-ec
		a.update(func() {
			sp.loading = false
			switch {
			case s.err != nil:
				sp.err = s.err
			case e.err != nil && !errors.Is(e.err, sdk.ErrAnonymousLimited):
				sp.err = e.err
			default:
				sp.series, sp.episodes = s.s, e.eps
				if sp.series.IsMultiSeason && sp.seasons == nil {
					a.loadSeasons(sp)
				}
				a.fillPending(sp)
			}
		})
	}()
}

// loadSeasons lists the seasons of a series in several, and gives the list
// to the page of each.
func (a *app) loadSeasons(sp *seriesPage) {
	if sp.seasonsLoading {
		return
	}
	sp.seasonsLoading = true
	go func() {
		ctx, cancel := contextTimeout(60 * time.Second)
		defer cancel()
		album, err := a.client.Seasons(ctx, sp.id)
		a.update(func() {
			sp.seasonsLoading = false
			if err != nil || album == nil || len(album.Seasons) < 2 {
				sp.seasons = []model.Season{} // one season: nothing to choose
				return
			}
			seasons := numberSeasons(album.Seasons)
			for _, se := range seasons {
				a.seriesEntry(se.SeriesID).seasons = seasons
			}
			sp.seasons = seasons
		})
	}()
}

// nextSeason is the season after this one, once it has episodes.
func (sp *seriesPage) nextSeason() (model.Season, bool) {
	for i, se := range sp.seasons {
		if se.SeriesID == sp.id && i+1 < len(sp.seasons) && sp.seasons[i+1].EpisodeCount > 0 {
			return sp.seasons[i+1], true
		}
	}
	return model.Season{}, false
}

// following is the episode after episode n: the next of the season, else
// the first of the next season.
func (a *app) following(sp *seriesPage, n int) (seriesID string, number int, ok bool) {
	if next, ok := sp.episode(n + 1); ok {
		return sp.id, next.Number, next.Playable
	}
	if ns, ok := sp.nextSeason(); ok {
		return ns.SeriesID, 1, true
	}
	return "", 0, false
}

func seasonLabel(se model.Season) string { return fmt.Sprintf("第 %d 季", se.Index) }

// seasonIndex is the current season's number when the series has several;
// a single season, or one whose place is not known, is zero.
func (sp *seriesPage) seasonIndex() int {
	if sp == nil {
		return 0
	}
	if len(sp.seasons) > 1 {
		for _, se := range sp.seasons {
			if se.SeriesID == sp.id {
				return se.Index
			}
		}
	}
	if sp.series != nil {
		return seasonNumber(sp.series.Title)
	}
	return 0
}

// seasonNumber reads the season a title names, as 持械入宋：第三季 or
// 浅浅归聿第二季.
func seasonNumber(title string) int {
	if m := seasonTitle.FindStringSubmatch(title); m != nil {
		return chineseNumber(m[2])
	}
	if m := seasonMark.FindAllStringSubmatch(title, -1); len(m) > 0 {
		return chineseNumber(m[len(m)-1][1])
	}
	return 0
}

// seasonMark is a season's number in its title: 第三季, 第 3 部.
var seasonMark = regexp.MustCompile(`第\s*([0-9一二两三四五六七八九十百零〇]+)\s*[季部]`)

// numberSeasons gives each season its number and puts them in order. The
// server sends the numbers to some devices and not to others; without
// them, the titles tell (持械入宋：第三季), and the one title without a
// number is the first season (浅浅归聿, 浅浅归聿第二季). Failing that, the
// seasons keep the server's order and are numbered by it.
func numberSeasons(in []model.Season) []model.Season {
	out := append([]model.Season(nil), in...)
	for i := range out {
		if out[i].Index <= 0 {
			out[i].Index = 0
			if m := seasonMark.FindAllStringSubmatch(out[i].Title, -1); m != nil {
				out[i].Index = chineseNumber(m[len(m)-1][1])
			}
		}
	}
	used := map[int]bool{}
	missing := -1
	distinct := true
	for i, se := range out {
		switch {
		case se.Index <= 0 && missing == -1:
			missing = i
		case se.Index <= 0:
			distinct = false // more than one without a number
		case used[se.Index]:
			distinct = false
		}
		used[se.Index] = true
	}
	if missing >= 0 && distinct && !used[1] {
		out[missing].Index = 1
	}
	ok := true
	seen := map[int]bool{}
	for _, se := range out {
		if se.Index <= 0 || seen[se.Index] {
			ok = false
			break
		}
		seen[se.Index] = true
	}
	if ok {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
		return out
	}
	for i := range out {
		out[i].Index = i + 1
	}
	return out
}

// seasonTitle matches a title that ends with its season, as 持械入宋：第三季.
var seasonTitle = regexp.MustCompile(`^(.*?)第([0-9一二两三四五六七八九十百零〇]+)季$`)

// orderSeasons puts the seasons of a series in order, where its first
// season is, and leaves the other cards where they are.
func orderSeasons(cards []card) {
	type info struct {
		group, season int
	}
	first := map[string]int{}
	infos := make(map[string]info, len(cards))
	for i, k := range cards {
		m := seasonTitle.FindStringSubmatch(k.Title)
		if m == nil {
			infos[k.ID] = info{group: i}
			continue
		}
		g, seen := first[m[1]]
		if !seen {
			g = i
			first[m[1]] = i
		}
		infos[k.ID] = info{group: g, season: chineseNumber(m[2])}
	}
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := infos[cards[i].ID], infos[cards[j].ID]
		if a.group != b.group {
			return a.group < b.group
		}
		return a.season < b.season
	})
}

// chineseNumber reads 3, 三, 十一 or 二十三.
func chineseNumber(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	n, cur := 0, 0
	for _, r := range s {
		switch r {
		case '十':
			n += max(cur, 1) * 10
			cur = 0
		case '百':
			n += max(cur, 1) * 100
			cur = 0
		default:
			cur = digits[r]
		}
	}
	return n + cur
}

func (sp *seriesPage) episode(n int) (model.Episode, bool) {
	for _, e := range sp.episodes {
		if e.Number == n {
			return e, true
		}
	}
	return model.Episode{}, false
}

// homeCache keeps the first page of recommendations, so the next launch
// shows them at once.
func (a *app) homeCachePath() string { return filepath.Join(a.cacheDir, "home.json") }

func (a *app) loadHomeCache() []card {
	data, err := os.ReadFile(a.homeCachePath())
	if err != nil {
		return nil
	}
	var cards []card
	_ = json.Unmarshal(data, &cards)
	return cards
}

func (a *app) saveHomeCache(cards []card) {
	cards = cards[:min(len(cards), 48)]
	go func() {
		data, err := json.Marshal(cards)
		if err != nil {
			return
		}
		tmp := a.homeCachePath() + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, a.homeCachePath())
		}
	}()
}

// friendly says what went wrong in the viewer's words.
func friendly(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, sdk.ErrTransport), errors.Is(err, context.DeadlineExceeded):
		return "网络连接不稳定，请稍后重试"
	case errors.Is(err, sdk.ErrRateLimited):
		return "请求太频繁，请稍等片刻再试"
	case errors.Is(err, sdk.ErrAnonymousLimited):
		return "这一集暂时无法匿名播放，可以稍后再试"
	case errors.Is(err, sdk.ErrAuthenticationRequired):
		return "这一集需要登录才能观看"
	case errors.Is(err, sdk.ErrContentUnavailable), errors.Is(err, sdk.ErrAlbumUnavailable):
		return "内容已下架或不存在"
	case errors.Is(err, sdk.ErrChallengeRequired), errors.Is(err, sdk.ErrDeviceRejected):
		return "服务器暂时拒绝了请求，请稍后再试"
	default:
		return err.Error()
	}
}

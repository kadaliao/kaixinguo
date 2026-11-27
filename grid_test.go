package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/sdk/model"
	"github.com/egoist/mygo/ui"
)

func TestGridStaysAtTop(t *testing.T) {
	a := testApp(t)
	a.home = newFeed(nil)
	a.home.started, a.home.more = true, true
	a.home.loading = true
	tt := ui.NewTester(a.view, 1180, 760)
	tt.Frame()
	var cards []card
	for i := 0; i < 24; i++ {
		cards = append(cards, card{ID: fmt.Sprint(i), Title: fmt.Sprintf("剧%02d", i), Sub: "1 集"})
	}
	a.home.loading = false
	a.home.add(page{cards: cards, more: true})
	a.home.loading = true // as loadMore would be
	tt.Frame()
	tt.Frame()
	r0, _ := tt.Find("剧00")
	t.Logf("after the first page: %v", r0)
	var more []card
	for i := 24; i < 48; i++ {
		more = append(more, card{ID: fmt.Sprint(i), Title: fmt.Sprintf("剧%02d", i), Sub: "1 集"})
	}
	a.home.loading = false
	a.home.add(page{cards: more, more: true})
	tt.Frame()
	tt.Frame()
	r1, ok := tt.Find("剧00")
	t.Logf("after the second page: %v %v", r1, ok)
	if !ok || r1.Y != r0.Y {
		t.Fatalf("the grid moved from %v to %v", r0, r1)
	}
}

// On Windows the window buttons fill the title bar's right end; what a
// page shows beside its title keeps clear of them.
func TestHeaderClearOfWindowButtons(t *testing.T) {
	a := testApp(t)
	a.rankContents = []model.RankContent{
		{ID: "a", Name: "真人剧", Boards: []model.RankBoard{{ID: "a1", Name: "热播榜"}}},
		{ID: "b", Name: "漫剧", Boards: []model.RankBoard{{ID: "b1", Name: "新剧榜"}}},
	}
	f := newFeed(nil)
	f.add(page{cards: a.home.cards})
	f.started, f.more = true, false
	a.feeds["rank::"] = f
	a.router.Push("/rank")
	tt := ui.NewTester(a.view, 1180, 760)
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Right: 138})
	tt.Advance(time.Second)
	if r, ok := tt.Find("漫剧"); !ok || r.X+r.W > 1180-138 {
		t.Fatalf("the contents' last segment at %v, under the window buttons", r)
	}
	tt.SetTitleBar(ui.TitleBar{Height: titleBarHeight, Left: 80})
	tt.Advance(time.Second)
	if r, ok := tt.Find("漫剧"); !ok || r.X+r.W < 1180-posterInset-30 {
		t.Fatalf("the contents' last segment at %v, not at the page's margin", r)
	}
}

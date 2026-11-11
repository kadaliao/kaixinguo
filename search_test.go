package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/adapters/memstore"
	"github.com/beilunyang/openhongguo/sdk"
)

const (
	tabGeneral = "11" // 综合
	tabSeries  = "29" // 剧集
)

// searchServer answers searches with tabs, which gives each tab_type's
// response. asked takes the tab_types searched since it was last called.
func searchServer(t *testing.T, tabs func(tab, offset string) string) (client *sdk.Client, asked func() []string) {
	t.Helper()
	var (
		mu        sync.Mutex
		tabsAsked []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/service/2/device_register/":
			_, _ = w.Write([]byte(`{"device_id_str":"7100000000000001","install_id_str":"7100000000000002"}`))
		case "/reading/bookapi/search/tab/v":
			tab := r.URL.Query().Get("tab_type")
			mu.Lock()
			tabsAsked = append(tabsAsked, tab)
			mu.Unlock()
			_, _ = w.Write([]byte(tabs(tab, r.URL.Query().Get("offset"))))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := sdk.NewClient(sdk.Options{
		Transport:          httptransport.New(),
		ProfileStore:       memstore.New(),
		APIOrigin:          srv.URL,
		RegisterOrigin:     srv.URL,
		DistributionOrigin: srv.URL,
		RetryAttempts:      1,
		DeviceResets:       sdk.Int(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := tabsAsked
		tabsAsked = nil
		return out
	}
}

// searchTab is a search response whose tab lists one series.
func searchTab(tab, id, title string, more bool) string {
	return fmt.Sprintf(`{"code":0,"message":"SUCCESS","search_tabs":[{"tab_type":%s,"has_more":%t,"next_offset":20,`+
		`"data":[{"video_data":{"series_id":%q,"video_detail":{"series_title":%q,"episode_cnt":100}}}]}]}`,
		tab, more, id, title)
}

// Some anonymous devices get an empty tab bar from the 剧集 tab, which used
// to make every search find nothing. The search asks 综合 instead, and
// stays there for the later pages.
func TestSearchFallsBackToGeneralWhenSeriesTabIsEmpty(t *testing.T) {
	client, asked := searchServer(t, func(tab, offset string) string {
		if tab == tabSeries {
			return `{"code":0,"message":"SUCCESS","search_tabs":[{"tab_type":11,"has_more":false,"data":[]}]}`
		}
		if offset == "20" {
			return searchTab(tab, "7675743518228220954", "浅浅归聿第二季", false)
		}
		return searchTab(tab, "7675743518228220953", "浅浅归聿", true)
	})
	a := testApp(t)
	a.client = client
	fetch := a.searchFetcher("归聿")
	p, err := fetch(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.cards) != 1 || p.cards[0].Title != "浅浅归聿" || !p.more {
		t.Fatalf("first page %+v, tabs %v", p, asked())
	}
	if tabs := asked(); !slices.Equal(tabs, []string{tabSeries, tabGeneral}) {
		t.Fatalf("the first page searched tabs %v, not 剧集 then 综合", tabs)
	}
	p, err = fetch(context.Background(), p.cursor)
	if err != nil {
		t.Fatal(err)
	}
	tabs := asked()
	if len(p.cards) != 1 || p.cards[0].Title != "浅浅归聿第二季" {
		t.Fatalf("second page %+v, tabs %v", p, tabs)
	}
	if !slices.Equal(tabs, []string{tabGeneral}) {
		t.Fatalf("the second page searched tabs %v, not 综合 alone", tabs)
	}
}

// The 剧集 tab lists every season, so a search that finds them there does
// not ask 综合, which lists the first alone.
func TestSearchListsSeasonsFromSeriesTab(t *testing.T) {
	client, asked := searchServer(t, func(tab, offset string) string {
		if tab == tabSeries {
			return searchTab(tab, "7671966197985856536", "持械入宋：第一季", false)
		}
		return searchTab(tab, "7697488144291250712", "持械入宋第十季预约开启", false)
	})
	a := testApp(t)
	a.client = client
	p, err := a.searchFetcher("持械入宋")(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	tabs := asked()
	if len(p.cards) != 1 || p.cards[0].Title != "持械入宋：第一季" {
		t.Fatalf("search cards %+v, tabs %v", p.cards, tabs)
	}
	if !slices.Equal(tabs, []string{tabSeries}) {
		t.Fatalf("searched tabs %v, not 剧集 alone", tabs)
	}
}

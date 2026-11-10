package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/adapters/memstore"
	"github.com/beilunyang/openhongguo/sdk"
)

// An anonymous search of the 剧集 tab comes back as an empty tab bar. That
// used to be treated as "nothing found", so every query missed the series
// the 综合 tab still lists.
func TestSearchUsesTheTabThatListsSeries(t *testing.T) {
	const (
		registerPath = "/service/2/device_register/"
		searchPath   = "/reading/bookapi/search/tab/v"
		series       = `{"code":0,"message":"SUCCESS","search_tabs":[{"tab_type":11,"has_more":false,"data":[{"video_data":{"series_id":"7675743518228220953","video_detail":{"series_title":"浅浅归聿","episode_cnt":124}}}]}]}`
		empty        = `{"code":0,"message":"SUCCESS","search_tabs":[{"tab_type":11,"has_more":false,"data":[]}]}`
	)
	var tabs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case registerPath:
			_, _ = w.Write([]byte(`{"device_id_str":"7100000000000001","install_id_str":"7100000000000002"}`))
		case searchPath:
			tab := r.URL.Query().Get("tab_type")
			tabs = append(tabs, tab)
			body := series
			if tab == "29" { // 剧集: an anonymous guest gets an empty tab bar
				body = empty
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

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
	a := testApp(t)
	a.client = client
	p, err := a.searchFetcher("归聿")(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.cards) != 1 || p.cards[0].Title != "浅浅归聿" {
		t.Fatalf("search cards %+v, tabs %v", p.cards, tabs)
	}
	for _, tab := range tabs {
		if tab == "29" {
			t.Fatalf("searched the empty 剧集 tab: %v", tabs)
		}
	}
}

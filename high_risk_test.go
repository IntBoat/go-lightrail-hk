package lightrail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseCatalogueRejectsBadInput(t *testing.T) {
	if _, err := parseCatalogue(nil); err == nil {
		t.Fatal("empty raw")
	}
	if _, err := parseCatalogue([]byte("a,b\n1,2\n")); err == nil {
		t.Fatal("missing columns")
	}
	badDir := []byte("Line Code,Direction,Stop Code,Stop ID,Chinese Name,English Name,Sequence\n505,x,SAS,920,三聖,Sam Shing,1\n")
	if _, err := parseCatalogue(badDir); err == nil {
		t.Fatal("bad direction")
	}
}

func TestParseCatalogueKeepsLowerDuplicateSequence(t *testing.T) {
	raw := []byte("\xEF\xBB\xBFLine Code,Direction,Stop Code,Stop ID,Chinese Name,English Name,Sequence\n" +
		"507,2,TNK,140,田景,Tin King,2\n" +
		"507,2,TNK,140,田景,Tin King,1\n" +
		"507,2,LEK,150,良景,Leung King,3\n")
	stops, err := parseCatalogue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(stops) != 2 {
		t.Fatalf("stops %d", len(stops))
	}
	if stops[0].ID != 140 || stops[0].Sequence != 1 {
		t.Fatalf("dedup %+v", stops[0])
	}
}

func TestDisplayRouteAndFilterRoute(t *testing.T) {
	if got := displayRoute(0, "610*", ""); got != "610" {
		t.Fatalf("regular %q", got)
	}
	if got := displayRoute(1, "901", "507*"); got != "507" {
		t.Fatalf("special %q", got)
	}
	if got := displayRoute(1, "902", "SPR"); got != "SPR" {
		t.Fatalf("spr %q", got)
	}
	rows := []Arrival{{Route: "761P"}, {Route: "610"}, {Route: "761p"}}
	if n := len(filterRoute(rows, "761P")); n != 2 {
		t.Fatalf("equal fold %d", n)
	}
	if n := len(filterRoute(rows, "")); n != 3 {
		t.Fatalf("all %d", n)
	}
}

func TestParseScheduleNullRemarksAndArriving(t *testing.T) {
	raw := []byte(`{
	  "status": 1,
	  "platform_list": [{
	    "platform_id": 1,
	    "route_list": [{
	      "train_length": 1,
	      "arrival_departure": "A",
	      "dest_en": "Siu Hong",
	      "dest_ch": "兆康",
	      "time_en": "Arriving",
	      "special": 1,
	      "route_no": "901",
	      "additionalInfo1": "507*",
	      "routeRemarkEng2": null,
	      "routeRemarkChi2": null
	    }]
	  }]
	}`)
	rows, err := parseSchedule(280, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	got := rows[0]
	if got.Status != StatusArriving || got.Route != "507" || !got.Special || got.RemarkEN != "" || got.RemarkZH != "" {
		t.Fatalf("%+v", got)
	}
}

func TestNextRejectsInvalidStopAndUpstreamErrors(t *testing.T) {
	c, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Next(context.Background(), 0, "", Call{}); err == nil {
		t.Fatal("stop 0")
	}
	if _, err := c.Next(context.Background(), -3, "", Call{}); err == nil {
		t.Fatal("negative stop")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down secret"))
	}))
	defer srv.Close()
	c.HTTP = srv.Client()
	c.schedURL = srv.URL
	_, err = c.Next(context.Background(), 600, "", Call{Fresh: true})
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("http err %v", err)
	}
}

func TestNextDoesNotCacheFailedParse(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"status":0}`))
	}))
	defer srv.Close()
	c, err := New(srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.schedURL = srv.URL
	if _, err := c.Next(context.Background(), 600, "", Call{}); err == nil {
		t.Fatal("expected status error")
	}
	if _, err := c.Next(context.Background(), 600, "", Call{}); err == nil {
		t.Fatal("expected status error again")
	}
	if hits.Load() != 2 {
		t.Fatalf("hits %d", hits.Load())
	}
}

func TestNextCacheReturnsIndependentCopies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(yuenLongFixture))
	}))
	defer srv.Close()
	c, err := New(srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.schedURL = srv.URL
	first, err := c.Next(context.Background(), 600, "761P", Call{})
	if err != nil {
		t.Fatal(err)
	}
	first[0].DestEN = "MUTATED"
	second, err := c.Next(context.Background(), 600, "761P", Call{})
	if err != nil {
		t.Fatal(err)
	}
	if second[0].DestEN != "Tin Yat" {
		t.Fatalf("cache mutated %+v", second[0])
	}
}

func TestRefreshCatalogueKeepsOldOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	c, err := New(srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	before := c.Stops("505", 1)
	if len(before) == 0 {
		t.Fatal("embedded empty")
	}
	c.catURL = srv.URL
	c.catAt = time.Now().Add(-catalogueTTL - time.Second)
	if err := c.RefreshCatalogue(context.Background(), Call{}); err == nil {
		t.Fatal("expected refresh error")
	}
	after := c.Stops("505", 1)
	if len(after) != len(before) || after[0].ID != before[0].ID {
		t.Fatalf("catalogue replaced on failure")
	}
}

func TestClientConcurrentNextAndCatalogue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(yuenLongFixture))
	}))
	defer srv.Close()
	c, err := New(srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.schedURL = srv.URL
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Next(context.Background(), 600, "761P", Call{}); err != nil {
				t.Errorf("next: %v", err)
			}
			_ = c.Routes()
			_ = c.Stops("761P", 2)
		}()
	}
	wg.Wait()
}

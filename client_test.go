package lightrail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const yuenLongFixture = `{
  "status": 1,
  "platform_list": [
    {"platform_id": 3, "route_list": [
      {"train_length": 2, "arrival_departure": "D", "dest_en": "Tuen Mun Ferry Pier", "dest_ch": "屯門碼頭", "time_en": "2 min", "special": 0, "route_no": "610"}
    ]},
    {"platform_id": 5, "route_list": [
      {"train_length": 2, "arrival_departure": "D", "dest_en": "Tin Yat", "dest_ch": "天逸", "time_en": "9 mins", "special": 0, "route_no": "761P"},
      {"train_length": 2, "arrival_departure": "D", "dest_en": "Tin Yat", "dest_ch": "天逸", "time_en": "19 mins", "special": 0, "route_no": "761P"},
      {"train_length": 1, "arrival_departure": "A", "dest_en": "Tin Yat", "dest_ch": "天逸", "time_en": "-", "special": 1, "route_no": "751", "additionalInfo1": "761P*"}
    ]}
  ]
}`

func TestEmbedded761PDropsDuplicateTerminus(t *testing.T) {
	c, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	stops := c.Stops("761P", 2)
	if len(stops) != 14 {
		t.Fatalf("stops %d", len(stops))
	}
	last := stops[len(stops)-1]
	if last.ID != 550 || last.NameZH != "天逸" {
		t.Fatalf("last %+v", last)
	}
	var tinYat int
	for _, stop := range stops {
		if stop.ID == 550 {
			tinYat++
		}
	}
	if tinYat != 1 {
		t.Fatalf("tin yat copies %d", tinYat)
	}
	if !contains(c.Routes(), "761P") || !contains(c.Routes(), "505") {
		t.Fatalf("routes %v", c.Routes())
	}
}

func TestNextKeepsSeveralTrainsAndSpecialRoute(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("with_special") != "1" || r.URL.Query().Get("station_id") != "600" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(yuenLongFixture))
	}))
	defer srv.Close()
	c, err := New(srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.schedURL = srv.URL
	rows, err := c.Next(context.Background(), 600, "761P", Call{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows %d", len(rows))
	}
	if rows[0].Minutes != 9 || rows[0].Status != StatusMinutes || rows[0].Platform != 5 {
		t.Fatalf("first %+v", rows[0])
	}
	if rows[1].Minutes != 19 || rows[1].DestZH != "天逸" {
		t.Fatalf("second %+v", rows[1])
	}
	if !rows[2].Special || rows[2].Route != "761P" || rows[2].Status != StatusUnknown {
		t.Fatalf("special %+v", rows[2])
	}
	if _, err := c.Next(context.Background(), 600, "610", Call{}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("cache hits %d", hits.Load())
	}
	if _, err := c.Next(context.Background(), 600, "761P", Call{Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("fresh hits %d", hits.Load())
	}
}

func TestParseTime(t *testing.T) {
	cases := []struct {
		in     string
		status Status
		mins   int
	}{
		{"2 min", StatusMinutes, 2},
		{"11 mins", StatusMinutes, 11},
		{"10 分鐘", StatusMinutes, 10},
		{"-", StatusUnknown, 0},
		{"Arriving", StatusArriving, 0},
		{"正在離開", StatusDeparting, 0},
	}
	for _, tc := range cases {
		status, mins := parseTime(tc.in)
		if status != tc.status || mins != tc.mins {
			t.Fatalf("%q -> %d %d", tc.in, status, mins)
		}
	}
}

func TestBadStatus(t *testing.T) {
	_, err := parseSchedule(468, []byte(`{"status":0,"platform_list":[]}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefreshCatalogueFreshSkipsWithinTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(embeddedRoutes)
	}))
	defer srv.Close()
	c, err := New(srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.catURL = srv.URL
	if err := c.RefreshCatalogue(context.Background(), Call{Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshCatalogue(context.Background(), Call{}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
	c.catAt = time.Now().Add(-catalogueTTL - time.Second)
	if err := c.RefreshCatalogue(context.Background(), Call{}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expired hits %d", hits.Load())
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

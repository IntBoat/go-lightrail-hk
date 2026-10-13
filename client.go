// Package lightrail reads Hong Kong Light Rail routes and live next-train times.
// There is no published clock timetable of every trip. Next returns the official
// per-platform list, which can contain several trains for the same route.
package lightrail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	scheduleURL  = "https://rt.data.gov.hk/v1/transport/mtr/lrt/getSchedule"
	catalogueURL = "https://opendata.mtr.com.hk/data/light_rail_routes_and_stops.csv"
	scheduleTTL  = 10 * time.Second
	catalogueTTL = 24 * time.Hour
	httpTimeout  = 8 * time.Second
)

// Status is how soon a train arrives. Minutes is set only for StatusMinutes.
type Status int

const (
	StatusMinutes Status = iota
	StatusArriving
	StatusDeparting
	StatusUnknown
)

// Arrival is one train on one platform. A station often has several.
type Arrival struct {
	StopID           int    `json:"stopId"`
	Platform         int    `json:"platform"`
	Route            string `json:"route"`
	DestEN           string `json:"destEn"`
	DestZH           string `json:"destZh"`
	Cars             int    `json:"cars"`
	ArrivalDeparture string `json:"arrivalDeparture"`
	Minutes          int    `json:"minutes,omitempty"`
	Status           Status `json:"status"`
	Special          bool   `json:"special"`
	RemarkEN         string `json:"remarkEn,omitempty"`
	RemarkZH         string `json:"remarkZh,omitempty"`
}

// Call controls one request. Fresh skips both the 10s schedule cache and the 24h catalogue cache.
type Call struct {
	Fresh bool
}

// Client is safe for concurrent use.
type Client struct {
	HTTP *http.Client
	// schedURL and catURL override the official endpoints. Empty means the public URLs.
	schedURL string
	catURL   string

	mu    sync.Mutex
	cat   []Stop
	catAt time.Time
	sched map[int]schedCache
}

type schedCache struct {
	at   time.Time
	rows []Arrival
}

// New loads the embedded route catalogue. Live times are fetched on Next.
func New(httpClient *http.Client) (*Client, error) {
	stops, err := parseCatalogue(embeddedRoutes)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: httpTimeout}
	}
	return &Client{HTTP: httpClient, cat: stops, sched: map[int]schedCache{}}, nil
}

// RefreshCatalogue downloads the official route CSV.
// Within 24 hours it returns the memory copy unless call.Fresh is set.
func (c *Client) RefreshCatalogue(ctx context.Context, call Call) error {
	c.mu.Lock()
	freshEnough := !call.Fresh && !c.catAt.IsZero() && time.Since(c.catAt) < catalogueTTL
	c.mu.Unlock()
	if freshEnough {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.catalogueEndpoint(), nil)
	if err != nil {
		return err
	}
	body, err := c.do(req)
	if err != nil {
		return err
	}
	stops, err := parseCatalogue(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cat = stops
	c.catAt = time.Now()
	c.mu.Unlock()
	return nil
}

// Next returns every upcoming train at stopID, in upstream platform order.
// route empty means all routes. The API has no direction argument; filter by DestEN or DestZH.
func (c *Client) Next(ctx context.Context, stopID int, route string, call Call) ([]Arrival, error) {
	if stopID <= 0 {
		return nil, fmt.Errorf("輕鐵車站編號無效")
	}
	if !call.Fresh {
		c.mu.Lock()
		hit, ok := c.sched[stopID]
		c.mu.Unlock()
		if ok && time.Since(hit.at) < scheduleTTL {
			return filterRoute(hit.rows, route), nil
		}
	}
	u := c.scheduleEndpoint() + "?station_id=" + strconv.Itoa(stopID) + "&with_special=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	rows, err := parseSchedule(stopID, body)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.sched[stopID] = schedCache{at: time.Now(), rows: rows}
	c.mu.Unlock()
	return filterRoute(rows, route), nil
}

func (c *Client) scheduleEndpoint() string {
	if c.schedURL != "" {
		return c.schedURL
	}
	return scheduleURL
}

func (c *Client) catalogueEndpoint() string {
	if c.catURL != "" {
		return c.catURL
	}
	return catalogueURL
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("輕鐵上游 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

type scheduleDoc struct {
	Status       int `json:"status"`
	PlatformList []struct {
		PlatformID int `json:"platform_id"`
		RouteList  []struct {
			TrainLength      int    `json:"train_length"`
			ArrivalDeparture string `json:"arrival_departure"`
			DestEN           string `json:"dest_en"`
			DestZH           string `json:"dest_ch"`
			AdditionalInfo1  string `json:"additionalInfo1"`
			RemarkEN         string `json:"routeRemarkEng2"`
			RemarkZH         string `json:"routeRemarkChi2"`
			TimeEN           string `json:"time_en"`
			Special          int    `json:"special"`
			RouteNo          string `json:"route_no"`
		} `json:"route_list"`
	} `json:"platform_list"`
}

func parseSchedule(stopID int, raw []byte) ([]Arrival, error) {
	var doc scheduleDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("輕鐵到站 JSON 無效: %w", err)
	}
	if doc.Status != 1 {
		return nil, fmt.Errorf("輕鐵到站狀態 %d", doc.Status)
	}
	var out []Arrival
	for _, platform := range doc.PlatformList {
		for _, train := range platform.RouteList {
			status, minutes := parseTime(train.TimeEN)
			out = append(out, Arrival{
				StopID:           stopID,
				Platform:         platform.PlatformID,
				Route:            displayRoute(train.Special, train.RouteNo, train.AdditionalInfo1),
				DestEN:           strings.TrimSpace(train.DestEN),
				DestZH:           strings.TrimSpace(train.DestZH),
				Cars:             train.TrainLength,
				ArrivalDeparture: strings.TrimSpace(train.ArrivalDeparture),
				Minutes:          minutes,
				Status:           status,
				Special:          train.Special != 0,
				RemarkEN:         strings.TrimSpace(train.RemarkEN),
				RemarkZH:         strings.TrimSpace(train.RemarkZH),
			})
		}
	}
	return out, nil
}

func displayRoute(special int, routeNo, additional string) string {
	s := strings.TrimSpace(routeNo)
	if special != 0 {
		s = strings.TrimSpace(additional)
	}
	s = strings.TrimRight(s, "*")
	return strings.TrimSpace(s)
}

func parseTime(raw string) (Status, int) {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "", "-":
		return StatusUnknown, 0
	case "arriving", "即將抵達":
		return StatusArriving, 0
	case "departing", "正在離開":
		return StatusDeparting, 0
	}
	fields := strings.Fields(s)
	if len(fields) == 2 {
		n, err := strconv.Atoi(fields[0])
		unit := strings.ToLower(fields[1])
		if err == nil && n >= 0 && (unit == "min" || unit == "mins" || unit == "分鐘") {
			return StatusMinutes, n
		}
	}
	return StatusUnknown, 0
}

func filterRoute(rows []Arrival, route string) []Arrival {
	route = strings.TrimSpace(route)
	out := make([]Arrival, 0, len(rows))
	for _, row := range rows {
		if route == "" || strings.EqualFold(row.Route, route) {
			out = append(out, row)
		}
	}
	return out
}

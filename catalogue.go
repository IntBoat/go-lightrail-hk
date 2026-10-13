package lightrail

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

//go:embed routes.csv
var embeddedRoutes []byte

// Stop is one stop on one route direction, from the official routes CSV.
type Stop struct {
	Route     string `json:"route"`
	Direction int    `json:"direction"`
	Code      string `json:"code"`
	ID        int    `json:"id"`
	NameZH    string `json:"nameZh"`
	NameEN    string `json:"nameEn"`
	Sequence  int    `json:"sequence"`
}

func parseCatalogue(raw []byte) ([]Stop, error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("輕鐵站序沒有標題列: %w", err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.TrimSpace(name)] = i
	}
	need := []string{"Line Code", "Direction", "Stop Code", "Stop ID", "Chinese Name", "English Name", "Sequence"}
	for _, name := range need {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("輕鐵站序缺少欄位 %s", name)
		}
	}
	type key struct {
		route string
		dir   int
		id    int
	}
	seen := map[key]int{}
	var stops []Stop
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("輕鐵站序讀取失敗: %w", err)
		}
		cell := func(name string) string {
			i := col[name]
			if i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		dir, err := strconv.Atoi(cell("Direction"))
		if err != nil {
			return nil, fmt.Errorf("輕鐵站序方向無效: %w", err)
		}
		id, err := strconv.Atoi(cell("Stop ID"))
		if err != nil {
			return nil, fmt.Errorf("輕鐵站序車站編號無效: %w", err)
		}
		seq, err := strconv.Atoi(cell("Sequence"))
		if err != nil {
			return nil, fmt.Errorf("輕鐵站序序號無效: %w", err)
		}
		stop := Stop{
			Route:     cell("Line Code"),
			Direction: dir,
			Code:      cell("Stop Code"),
			ID:        id,
			NameZH:    cell("Chinese Name"),
			NameEN:    cell("English Name"),
			Sequence:  seq,
		}
		k := key{stop.Route, stop.Direction, stop.ID}
		if prev, ok := seen[k]; ok {
			if stop.Sequence >= stops[prev].Sequence {
				continue
			}
			stops[prev] = stop
			continue
		}
		seen[k] = len(stops)
		stops = append(stops, stop)
	}
	if len(stops) == 0 {
		return nil, fmt.Errorf("輕鐵站序是空的")
	}
	return stops, nil
}

// Routes lists route numbers in the current catalogue, in first-seen order.
func (c *Client) Routes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, stop := range c.cat {
		if seen[stop.Route] {
			continue
		}
		seen[stop.Route] = true
		out = append(out, stop.Route)
	}
	return out
}

// Stops returns one direction of a route, ordered by sequence.
// Direction is the official CSV column (1 or 2), not a destination stop id.
func (c *Client) Stops(route string, direction int) []Stop {
	route = strings.TrimSpace(route)
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Stop
	for _, stop := range c.cat {
		if stop.Route == route && stop.Direction == direction {
			out = append(out, stop)
		}
	}
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j].Sequence < out[j-1].Sequence {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
	return out
}

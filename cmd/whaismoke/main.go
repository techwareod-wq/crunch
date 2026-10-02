// Command whaismoke runs a handful of natural-language searches against a
// running service and prints what the AI parsed (spec 05; manual, not CI —
// every query costs a model call).
//
//	go run ./cmd/whaismoke                                  # localhost:3090, built-in queries
//	go run ./cmd/whaismoke -api https://api.example.com -q "cold storage near 411001"
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var defaultQueries = []string{
	"cold storage near Chakan Pune, at least 10k sqft",
	"pharma warehouse 400703 under ₹40/sqft",
	"thanda godown around 5000 sq m bhiwandi",
	"hazmat storage within 50 km of Ahmedabad budget 5 lakh per month",
	"cheapest warehouse in Bangalore",
	"spacious place for my furniture business",
}

type queries []string

func (q *queries) String() string     { return strings.Join(*q, "; ") }
func (q *queries) Set(v string) error { *q = append(*q, v); return nil }

func main() {
	api := flag.String("api", "http://localhost:3090", "service base URL")
	var qs queries
	flag.Var(&qs, "q", "query to run (repeatable; default: built-in set)")
	flag.Parse()
	if len(qs) == 0 {
		qs = defaultQueries
	}
	client := &http.Client{Timeout: 15 * time.Second}
	failed := 0
	for _, q := range qs {
		if err := run(client, *api, q); err != nil {
			failed++
			fmt.Printf("✗ %q: %v\n\n", q, err)
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func run(client *http.Client, api, q string) error {
	body, _ := json.Marshal(map[string]any{"q": q})
	start := time.Now()
	resp, err := client.Post(strings.TrimRight(api, "/")+"/v1/public/search/ai", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Data struct {
			AI      map[string]any `json:"ai"`
			Applied struct {
				Filters       map[string]any `json:"filters"`
				GeocodeSource string         `json:"geocodeSource"`
				Dropped       []string       `json:"dropped"`
			} `json:"applied"`
			Radius   map[string]any `json:"radius"`
			Total    int            `json:"total"`
			Fallback map[string]any `json:"fallback"`
			Degraded []string       `json:"degraded"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	d := out.Data
	filters, _ := json.MarshalIndent(d.Applied.Filters, "    ", "  ")
	fmt.Printf("✓ %q (%v round trip)\n", q, time.Since(start).Round(time.Millisecond))
	fmt.Printf("  ai: parsed=%v model=%v latency=%vms notes=%v\n", d.AI["parsed"], d.AI["model"], d.AI["latencyMs"], d.AI["notes"])
	fmt.Printf("  filters: %s\n", filters)
	fmt.Printf("  geocode=%q dropped=%v radius=%v total=%d degraded=%v\n", d.Applied.GeocodeSource, d.Applied.Dropped, d.Radius, d.Total, d.Degraded)
	if d.Fallback != nil {
		n := 0
		if r, ok := d.Fallback["results"].([]any); ok {
			n = len(r)
		}
		fmt.Printf("  fallback: reason=%v source=%v results=%d\n", d.Fallback["reason"], d.Fallback["source"], n)
	}
	fmt.Println()
	return nil
}

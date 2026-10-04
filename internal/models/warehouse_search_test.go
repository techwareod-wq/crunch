package models

import (
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func f(v float64) *float64 { return &v }

// js renders v as canonical JSON for comparisons.
func js(t *testing.T, v any) string {
	t.Helper()
	b, err := bson.MarshalExtJSON(bson.M{"v": v}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSearchMatch(t *testing.T) {
	chip := SearchChip{Chip: "cold_storage.temp_type:frozen", Unk: []string{"cold_storage.temp_type", "cold_storage"}}
	rng := SearchRange{Conds: []NumCond{{K: "cold_storage.range_max", Gte: f(-20)}, {K: "cold_storage.range_min", Lte: f(-10)}}, Unk: []string{"cold_storage.range"}}
	cases := []struct {
		name string
		q    SearchQuery
		want []string // substrings of the rendered match
		not  []string
	}{
		{"empty is live only", SearchQuery{}, []string{`"status":"live"`}, []string{"$and", "country"}},
		{"country + area", SearchQuery{Country: "IN", AreaMin: f(100)}, []string{`"country":"IN"`, `"total_sqm":{"$gte":100`}, []string{"$lte"}},
		{"price passes on-request", SearchQuery{PriceCurrency: "INR", PriceMax: f(500)},
			[]string{`"price.currency":"INR"`, `"price.per_sqm_month":{"$lte":500`, `"price.on_request":true`}, nil},
		{"chip known only", SearchQuery{Chips: []SearchChip{chip}}, []string{`{"chips":"cold_storage.temp_type:frozen"}`}, []string{"unk"}},
		{"chip + unverified", SearchQuery{Chips: []SearchChip{chip}, IncludeUnverified: true},
			[]string{`"$or":[{"chips":"cold_storage.temp_type:frozen"},{"unk":{"$in":["cold_storage.temp_type","cold_storage"]}}]`}, nil},
		{"range field needs both bounds", SearchQuery{Ranges: []SearchRange{rng}},
			[]string{`"k":"cold_storage.range_max","v":{"$gte":-20`, `"k":"cold_storage.range_min","v":{"$lte":-10`, `"$and":[{"nums"`}, []string{"unk"}},
		{"range + unverified", SearchQuery{Ranges: []SearchRange{rng}, IncludeUnverified: true}, []string{`{"unk":{"$in":["cold_storage.range"]}}`}, nil},
		{"industries AND", SearchQuery{Industries: []string{"pharma", "food"}},
			[]string{`{"fit":{"$in":["pharma:F","pharma:P"]}}`, `{"fit":{"$in":["food:F","food:P"]}}`}, []string{":U"}},
		{"industries + unverified", SearchQuery{Industries: []string{"pharma"}, IncludeUnverified: true}, []string{`"pharma:U"`}, nil},
		{"exclude", SearchQuery{Exclude: primitive.NewObjectID()}, []string{`"_id":{"$ne"`}, nil},
		{"statuses widen", SearchQuery{Statuses: []string{WarehouseLive, WarehouseArchived}}, []string{`"status":{"$in":["live","archived"]}`}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := js(t, SearchMatch(c.q))
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s in %s", w, got)
				}
			}
			for _, w := range c.not {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %s in %s", w, got)
				}
			}
		})
	}
}

func sortKeys(t *testing.T, s SearchSort) []string {
	t.Helper()
	stages := SearchSortStages(s)
	var keys []string
	for _, st := range stages {
		if st[0].Key == "$sort" {
			for _, e := range st[0].Value.(bson.D) {
				b, _ := json.Marshal(e.Value)
				keys = append(keys, e.Key+":"+string(b))
			}
		}
	}
	return keys
}

func TestSearchSortStages(t *testing.T) {
	cases := []struct {
		s    SearchSort
		want string
	}{
		{SearchSort{Mode: "distance", Near: true}, "dist_m:1,_id:1"},
		{SearchSort{Mode: "distance", Near: true, PriceFilter: true}, "_po:1,dist_m:1,_id:1"},
		{SearchSort{Mode: "relevance", Near: true}, "_score:-1,dist_m:1,_id:1"},
		{SearchSort{Mode: "relevance"}, "_score:-1,_id:1"},
		{SearchSort{Mode: "price_asc"}, "_po:1,price.per_sqm_month:1,_id:1"},
		{SearchSort{Mode: "price_desc"}, "_po:1,price.per_sqm_month:-1,_id:1"},
		{SearchSort{Mode: "area_desc"}, "total_sqm:-1,_id:1"},
	}
	for _, c := range cases {
		if got := strings.Join(sortKeys(t, c.s), ","); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.s, got, c.want)
		}
	}
	// The relevance tier only looks at the selected industries.
	got := js(t, SearchSortStages(SearchSort{Mode: "relevance", Industries: []string{"pharma"}}))
	if !strings.Contains(got, `"$setIsSubset":[["pharma:F"]`) || !strings.Contains(got, `"pharma:P"`) {
		t.Errorf("tier = %s", got)
	}
}

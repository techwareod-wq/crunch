package main

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := "IN\t411001\tPune City\tMaharashtra\t16\tPune\t521\t\t\t18.0\t73.0\t4\n" +
		"IN\t411001\tCamp\tMaharashtra\t16\tPune\t521\t\t\t20.0\t75.0\t4\n" +
		"IN\t400001\tFort\tMaharashtra\t16\tMumbai\t518\t\t\t18.9\t72.8\t4\n" +
		"bad row\n" +
		"IN\t999999\tNowhere\tX\t1\tY\t2\t\t\tnotanumber\t1\t1\n"
	pins, skipped, err := parse(strings.NewReader(in))
	if err != nil || skipped != 2 || len(pins) != 2 {
		t.Fatalf("pins=%d skipped=%d err=%v", len(pins), skipped, err)
	}
	p := pins[0]
	if p.Code != "411001" || p.Lat != 19 || p.Lng != 74 || p.Place != "Pune City" || p.DistrictLC != "pune" {
		t.Errorf("merged = %+v", p)
	}
	if len(p.Places) != 2 || p.Places[1] != "camp" {
		t.Errorf("places = %v", p.Places)
	}
}

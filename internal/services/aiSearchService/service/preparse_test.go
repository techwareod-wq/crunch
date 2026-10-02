package service

import "testing"

func TestPreparse(t *testing.T) {
	cases := []struct {
		q     string
		pin   string
		area  *areaHint
		price *priceHint
	}{
		{q: "10k sqft cold storage in pune", area: &areaHint{10000, "sqft", "min"}},
		{q: "warehouse 1,000 sq. ft. near chakan", area: &areaHint{1000, "sqft", "min"}},
		{q: "godown under ₹150/sqft", price: &priceHint{150, "per_sqft_month", "max"}},
		{q: "rent 5 lakh per month bhiwandi", price: &priceHint{500000, "flat_month", "max"}},
		{q: "pharma warehouse 400703", pin: "400703"},
		{q: "around 5000 sqm near 411001 under Rs. 40 per sq ft", pin: "411001",
			area: &areaHint{5000, "sqm", "approx"}, price: &priceHint{40, "per_sqft_month", "max"}},
		{q: "up to 20,000 sqft at least INR 2.5 cr", area: &areaHint{20000, "sqft", "max"}, price: &priceHint{25000000, "flat_month", "min"}},
		{q: "250000 sqft in 560001", pin: "560001", area: &areaHint{250000, "sqft", "min"}},
		{q: "cold storage thanda godown"},
	}
	for _, c := range cases {
		h := preparse(c.q)
		if h.Pincode != c.pin {
			t.Errorf("%q pin = %q", c.q, h.Pincode)
		}
		if (h.Area == nil) != (c.area == nil) || (h.Area != nil && *h.Area != *c.area) {
			t.Errorf("%q area = %+v want %+v", c.q, h.Area, c.area)
		}
		if (h.Price == nil) != (c.price == nil) || (h.Price != nil && *h.Price != *c.price) {
			t.Errorf("%q price = %+v want %+v", c.q, h.Price, c.price)
		}
	}
}

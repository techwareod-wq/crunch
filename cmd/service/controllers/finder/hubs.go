package finder

import (
	"math"
	"slices"
)

// APPROXIMATE coordinates, for testing only. Replace with real listing
// attributes (near_port / near_rail / near_airport) if the feature is kept.

// hub is one port, airport or rail terminal.
type hub struct {
	Name     string
	Lat, Lng float64
}

// Near-hub radii (km).
const (
	nearPortKm    = 75
	nearAirportKm = 50
	nearRailKm    = 25
)

// ports are the major Indian seaports.
var ports = []hub{
	{"JNPT Nhava Sheva", 18.95, 72.95},
	{"Mumbai Port", 18.94, 72.84},
	{"Mundra", 22.74, 69.70},
	{"Deendayal Kandla", 23.03, 70.22},
	{"Pipavav", 20.91, 71.51},
	{"Hazira", 21.10, 72.65},
	{"Dahej", 21.70, 72.55},
	{"Mangalore", 12.92, 74.81},
	{"Kochi", 9.97, 76.26},
	{"Tuticorin", 8.76, 78.19},
	{"Chennai", 13.10, 80.30},
	{"Kamarajar Ennore", 13.25, 80.33},
	{"Krishnapatnam", 14.25, 80.12},
	{"Visakhapatnam", 17.69, 83.29},
	{"Paradip", 20.26, 86.67},
	{"Haldia", 22.03, 88.07},
}

// APPROXIMATE coordinates, for testing only (see above).

// airports are the main cargo airports.
var airports = []hub{
	{"Mumbai CSMIA", 19.09, 72.87},
	{"Delhi IGI", 28.56, 77.10},
	{"Bengaluru Kempegowda", 13.20, 77.71},
	{"Chennai", 12.99, 80.17},
	{"Hyderabad Rajiv Gandhi", 17.24, 78.43},
	{"Kolkata", 22.65, 88.45},
	{"Pune", 18.58, 73.92},
	{"Ahmedabad", 23.08, 72.63},
	{"Kochi", 10.15, 76.40},
	{"Nagpur", 21.09, 79.05},
}

// rail are inland container / rail freight terminals.
var rail = []hub{
	{"Tughlakabad Delhi", 28.50, 77.28},
	{"Dadri", 28.58, 77.57},
	{"Loni Ghaziabad", 28.75, 77.28},
	{"Garhi Harsaru Gurugram", 28.43, 77.00},
	{"Khodiyar Ahmedabad", 23.05, 72.50},
	{"Whitefield Bengaluru", 12.98, 77.75},
	{"Sanathnagar Hyderabad", 17.46, 78.43},
	{"Tondiarpet Chennai", 13.13, 80.29},
	{"Dankuni Kolkata", 22.68, 88.29},
}

// earthKm is the mean Earth radius, shared by haversineKm and the Mongo
// $centerSphere prefilter so both agree on the radius.
const earthKm = 6371.0

// haversineKm is the great-circle distance between two WGS84 points.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	rad := math.Pi / 180
	dLat, dLng := (lat2-lat1)*rad, (lng2-lng1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// nearAny reports whether (lat, lng) is within km of any hub.
func nearAny(lat, lng float64, hubs []hub, km float64) bool {
	return slices.ContainsFunc(hubs, func(h hub) bool { return haversineKm(lat, lng, h.Lat, h.Lng) <= km })
}

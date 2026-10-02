// Package geocode is the Google Geocoding API client (D-061, D-078).
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

type google struct {
	client  interfaces.ApiClient
	apiURL  string
	key     string
	timeout time.Duration
}

var _ interfaces.Geocoder = (*google)(nil)

// NewGoogle returns a Google geocoder. Each call is bounded by timeout on top
// of the shared client's own timeout.
func NewGoogle(client interfaces.ApiClient, apiURL, key string, timeout time.Duration) interfaces.Geocoder {
	return &google{client: client, apiURL: apiURL, key: key, timeout: timeout}
}

type googleResponse struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
	Results      []struct {
		FormattedAddress string `json:"formatted_address"`
		PlaceID          string `json:"place_id"`
		Geometry         struct {
			Location struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			} `json:"location"`
			LocationType string `json:"location_type"`
		} `json:"geometry"`
	} `json:"results"`
}

func (g *google) Geocode(ctx context.Context, address, country string) (interfaces.GeocodeResult, error) {
	if g.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.timeout)
		defer cancel()
	}
	q := url.Values{}
	q.Set("address", address)
	q.Set("key", g.key)
	if country != "" {
		q.Set("components", "country:"+country)
	}
	body, status, err := g.client.Get(ctx, g.apiURL+"?"+q.Encode(), nil)
	if err != nil {
		return interfaces.GeocodeResult{}, fmt.Errorf("geocode: %w", err)
	}
	if status != http.StatusOK {
		return interfaces.GeocodeResult{}, fmt.Errorf("geocode: http %d", status)
	}
	var resp googleResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return interfaces.GeocodeResult{}, fmt.Errorf("geocode: decode: %w", err)
	}
	switch resp.Status {
	case "OK":
	case "ZERO_RESULTS":
		return interfaces.GeocodeResult{}, interfaces.ErrGeocodeNotFound
	default:
		return interfaces.GeocodeResult{}, fmt.Errorf("geocode: status %s: %s", resp.Status, resp.ErrorMessage)
	}
	if len(resp.Results) == 0 {
		return interfaces.GeocodeResult{}, interfaces.ErrGeocodeNotFound
	}
	r := resp.Results[0]
	return interfaces.GeocodeResult{
		Lat:       r.Geometry.Location.Lat,
		Lng:       r.Geometry.Location.Lng,
		PlaceID:   r.PlaceID,
		Accuracy:  r.Geometry.LocationType,
		Formatted: r.FormattedAddress,
	}, nil
}

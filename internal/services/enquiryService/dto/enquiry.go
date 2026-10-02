package dto

import (
	"encoding/csv"
	"io"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/models"
)

// EnquiryDetail is GET /v1/admin/enquiries/detail: the enquiry, the linked
// listing as it is now, and the search that led to it.
type EnquiryDetail struct {
	Enquiry *models.Enquiry `json:"enquiry"`
	// Listing is nil for a general requirement or a since-deleted listing.
	Listing *ListingCard `json:"listing"`
	// Search is nil when the enquiry carries no searchId or the raw search
	// expired (90 days, D-106).
	Search *OriginSearch `json:"search"`
}

// ListingCard is the linked listing's current state.
type ListingCard struct {
	WarehouseID string  `json:"warehouseId"`
	ShortID     string  `json:"shortId"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	City        string  `json:"city,omitempty"`
	Status      string  `json:"status"`
	TotalSqm    float64 `json:"totalSqm"`
	CoverURL    string  `json:"coverUrl,omitempty"`
	// PublicURL is empty until the public site's base URL is set (D-005)
	// or when the listing isn't live.
	PublicURL string `json:"publicUrl,omitempty"`
}

// OriginSearch is the search the enquiry came from.
type OriginSearch struct {
	SearchID    string    `json:"searchId"`
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	Text        string    `json:"text,omitempty"`
	Place       string    `json:"place,omitempty"`
	Filters     bson.M    `json:"filters"`
	ResultCount int64     `json:"resultCount"`
}

var csvHeader = []string{
	"id", "createdAt", "status", "closeReason", "name", "company", "email", "phone",
	"listingShortId", "listingName", "listingCity", "assignee", "message", "searchId", "sessionId",
}

// WriteCSV writes the inbox export.
func WriteCSV(w io.Writer, items []models.Enquiry) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, e := range items {
		var shortID, name, city, searchID string
		if l := e.Listing; l != nil {
			shortID, name, city = l.ShortID, l.Name, l.City
		}
		if e.SearchID != nil {
			searchID = e.SearchID.Hex()
		}
		row := []string{
			e.ID.Hex(), e.CreatedAt.UTC().Format(time.RFC3339), e.Status, e.CloseReason,
			e.Name, e.Company, e.Email, e.PhoneE164, shortID, name, city, e.AssigneeEmail,
			e.Message, searchID, e.SessionID,
		}
		for i := range row {
			row[i] = csvSafe(row[i])
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// csvSafe defuses spreadsheet formulas in visitor-typed cells.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

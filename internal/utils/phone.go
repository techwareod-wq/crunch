package utils

import (
	"errors"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// DefaultPhoneRegion is the region assumed for numbers written without a
// country code (D-100: India at launch).
const DefaultPhoneRegion = "IN"

// ErrInvalidPhone is returned by NormalizePhone for a number that does not
// parse or is not a valid number for its region.
var ErrInvalidPhone = errors.New("invalid phone number")

// NormalizePhone parses raw (with or without a +country prefix; region is the
// default when there is none) and returns it in E.164. Format check only — no
// OTP (D-100).
func NormalizePhone(raw, region string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInvalidPhone
	}
	num, err := phonenumbers.Parse(raw, region)
	if err != nil || !phonenumbers.IsValidNumber(num) {
		return "", ErrInvalidPhone
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}

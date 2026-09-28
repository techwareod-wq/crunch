package pipeline

import "errors"

// ErrPermanent marks an async handler failure that no retry can fix (bad
// credentials, unmappable required field, …). Process handlers wrap such
// failures with this sentinel (checked via errors.Is in the async handler's
// failure path) so the message goes straight to the DLQ instead of burning
// MaxRetries on a foregone outcome.
//
// Lives here rather than in the asyncHandler package so any service package
// can mark a failure permanent without importing the handler that runs it.
var ErrPermanent = errors.New("permanent process failure")

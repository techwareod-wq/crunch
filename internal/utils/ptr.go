package utils

// Deref returns the value pointed to by p, or the zero value of T when p is
// nil. It centralises the nil-check-then-dereference pattern used across the
// services for optional pointer fields (e.g. *string business-context fields)
// so callers can inline the deref without repeating the guard.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// Ptr returns a pointer to v. It is the generic replacement for the per-type
// *T constructors (e.g. the old models.IntPtr) used to build pointer fields on
// partial-update request structs.
func Ptr[T any](v T) *T { return &v }

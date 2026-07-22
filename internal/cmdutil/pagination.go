package cmdutil

import "fmt"

// MaxPageSize is the largest page the public API accepts without silently
// clamping the caller's request.
const MaxPageSize = 100

// ValidatePageLimit keeps list commands honest about the number of results
// requested. Callers that need more than one page should use --paginate.
func ValidatePageLimit(limit int) error {
	if limit < 1 || limit > MaxPageSize {
		return FlagError{Err: fmt.Errorf("--limit must be between 1 and %d", MaxPageSize)}
	}
	return nil
}

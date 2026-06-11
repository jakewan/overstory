package github

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestRateLimitedErrorMatchesSentinel pins the dual contract the typed rate-limit
// error must honor: errors.Is keeps matching the ErrRateLimited sentinel (so
// existing classification checks stay valid) while errors.As recovers the reset
// detail — and both survive the %w wrap the handler applies. errors.As is matched
// against a value target on purpose: the error propagates by value, and a stray
// pointer representation would silently fail this extraction.
func TestRateLimitedErrorMatchesSentinel(t *testing.T) {
	reset := time.Date(2026, 6, 9, 0, 15, 0, 0, time.UTC)
	base := error(RateLimitedError{ResetAt: reset, RetryAfter: 30 * time.Second})

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unwrapped", base},
		{"wrapped", fmt.Errorf("fetching issues for acme/widgets: %w", base)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, ErrRateLimited) {
				t.Errorf("errors.Is(%v, ErrRateLimited) = false, want true", tc.err)
			}
			var rle RateLimitedError
			if !errors.As(tc.err, &rle) {
				t.Fatalf("errors.As(%v, &RateLimitedError) = false, want true", tc.err)
			}
			if !rle.ResetAt.Equal(reset) {
				t.Errorf("recovered ResetAt = %v, want %v", rle.ResetAt, reset)
			}
			if rle.RetryAfter != 30*time.Second {
				t.Errorf("recovered RetryAfter = %v, want 30s", rle.RetryAfter)
			}
		})
	}
}

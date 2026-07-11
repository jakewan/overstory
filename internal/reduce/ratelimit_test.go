package reduce

import (
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

func TestAggregateBudget(t *testing.T) {
	early := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	late := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("empty input yields nil", func(t *testing.T) {
		if got := AggregateBudget(nil); got != nil {
			t.Errorf("AggregateBudget(nil) = %+v, want nil", got)
		}
	})

	t.Run("no budget and no throttle yields nil", func(t *testing.T) {
		if got := AggregateBudget([]BudgetSource{{}, {}}); got != nil {
			t.Errorf("AggregateBudget = %+v, want nil", got)
		}
	})

	t.Run("throttle anywhere wins with earliest reset", func(t *testing.T) {
		// A throttle overrides any successful budget; Remaining is the 0 marker and
		// the reset is the earliest across throttled sources.
		got := AggregateBudget([]BudgetSource{
			{RateLimit: &github.RateLimit{Remaining: 500, ResetAt: late}},
			{RateLimited: true, ResetAt: late},
			{RateLimited: true, ResetAt: early},
		})
		if got == nil || got.Remaining != 0 || !got.ResetAt.Equal(early) {
			t.Errorf("got %+v, want Remaining 0 / ResetAt %v", got, early)
		}
	})

	t.Run("tightest budget, tie broken by earliest reset", func(t *testing.T) {
		got := AggregateBudget([]BudgetSource{
			{RateLimit: &github.RateLimit{Remaining: 100, ResetAt: late}},
			{RateLimit: &github.RateLimit{Remaining: 50, ResetAt: late}},
			{RateLimit: &github.RateLimit{Remaining: 50, ResetAt: early}},
		})
		if got == nil || got.Remaining != 50 || !got.ResetAt.Equal(early) {
			t.Errorf("got %+v, want Remaining 50 / ResetAt %v", got, early)
		}
	})

	t.Run("unavailable-not-throttled source contributes nothing", func(t *testing.T) {
		// Neither throttled nor carrying a budget (e.g. not_found): skipped, so the
		// lone real budget is reported unchanged.
		got := AggregateBudget([]BudgetSource{
			{},
			{RateLimit: &github.RateLimit{Remaining: 200, ResetAt: late}},
		})
		if got == nil || got.Remaining != 200 || !got.ResetAt.Equal(late) {
			t.Errorf("got %+v, want Remaining 200 / ResetAt %v", got, late)
		}
	})
}

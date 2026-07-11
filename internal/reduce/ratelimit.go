package reduce

import (
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// RateLimitFacts is the rate-limit budget snapshot from a fetch, so a caller can
// pace itself: the units Remaining in the current window and the ResetAt instant
// it refills. The units are GraphQL points or REST core requests depending on the
// fetching tool's pool — the two are never mixed within one fact. It is shared
// across reductions because every fetch carries the same budget shape; a
// reduction's Facts root embeds it as a pointer and omits it (nil) when the fetch
// carried no budget block, so a caller never mistakes an unknown budget for a
// present one.
type RateLimitFacts struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

// BudgetSource is one fetch's contribution to a batch's aggregated pacing signal:
// either a throttle (RateLimited, with the ResetAt the fetch reported) or a
// successful reading (RateLimit non-nil). A source that is unavailable for some
// other reason carries neither and contributes nothing.
type BudgetSource struct {
	RateLimited bool
	ResetAt     time.Time
	RateLimit   *github.RateLimit
}

// AggregateBudget reduces a batch's per-fetch budgets to the single pacing signal
// a caller should heed: a throttle anywhere wins (Remaining 0 with the earliest
// known reset, so a caller is never told it has budget while GitHub is throttling
// — the 0 is a throttle marker, not a literal reading); otherwise the tightest
// successful budget (smallest Remaining, ties broken by the earliest reset);
// otherwise nil when no source carried a budget. It is pool-agnostic and is
// invoked once per batch, so a caller must never feed it sources drawn from
// different pools (GraphQL points and REST core) together.
func AggregateBudget(sources []BudgetSource) *RateLimitFacts {
	throttled := false
	var earliestReset time.Time
	for _, s := range sources {
		if !s.RateLimited {
			continue
		}
		throttled = true
		if !s.ResetAt.IsZero() && (earliestReset.IsZero() || s.ResetAt.Before(earliestReset)) {
			earliestReset = s.ResetAt
		}
	}
	if throttled {
		return &RateLimitFacts{Remaining: 0, ResetAt: earliestReset}
	}

	var tightest *github.RateLimit
	for _, s := range sources {
		if s.RateLimit == nil {
			continue
		}
		b := s.RateLimit
		if tightest == nil || b.Remaining < tightest.Remaining ||
			(b.Remaining == tightest.Remaining && b.ResetAt.Before(tightest.ResetAt)) {
			tightest = b
		}
	}
	if tightest == nil {
		return nil
	}
	return &RateLimitFacts{Remaining: tightest.Remaining, ResetAt: tightest.ResetAt}
}

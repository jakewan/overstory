package authored

import (
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// Per-repo unavailability reasons in a batch. The fan-out classifies each repo's
// fetch outcome into one of these; an available repo carries none. They live here
// (not in the server) so the reduction and the handler that fills BatchEntry share
// one vocabulary. UnavailableAuthorNotFound is an internal sentinel only: the
// handler converts it to a single whole-batch error (the author login is
// repo-independent, so an unresolvable login fails every repo), and it never
// reaches a returned RepoActivity.
const (
	UnavailableNotFound       = "not_found"
	UnavailableRateLimited    = "rate_limited"
	UnavailableFetchFailed    = "fetch_failed"
	UnavailableAuthorNotFound = "author_not_found"
)

// BatchEntry is one repo's fan-out outcome, the neutral input the server fills
// for the pure reduction: a successful Result (with its own RateLimit budget) when
// Unavailable is empty, or an Unavailable reason (with ResetAt set for a throttle)
// otherwise.
type BatchEntry struct {
	Repo        string
	Result      github.AuthoredActivityResult
	Unavailable string
	ResetAt     time.Time
}

// BatchFacts is the batch tool's output: one batch-level identity (the author,
// the shared window, the generation time) and a per-repo entry list, plus the
// aggregated budget. There is no cross-repo roll-up of the counts — summing,
// ranking, and the attention verdict stay caller-side. GeneratedAt is stamped by
// the handler, mirroring the single-repo Facts. RateLimit is omitted when no fetch
// carried a budget and none throttled.
type BatchFacts struct {
	Author      string                 `json:"author"`
	Since       time.Time              `json:"since"`
	Until       time.Time              `json:"until"`
	GeneratedAt time.Time              `json:"generatedAt"`
	Repos       []RepoActivity         `json:"repos"`
	RateLimit   *reduce.RateLimitFacts `json:"rateLimit,omitempty"`
}

// RepoActivity is one repo's slot in the batch result: either Available with its
// six Counts, or unavailable with a reason (and, for a throttle, the ResetAt the
// caller can retry at). Counts and ResetAt are pointers so they omit cleanly in
// the case they don't apply. Available is always emitted so a caller branches on
// it explicitly rather than inferring from a missing field.
type RepoActivity struct {
	Repo        string     `json:"repo"`
	Available   bool       `json:"available"`
	Unavailable string     `json:"unavailable,omitempty"`
	ResetAt     *time.Time `json:"resetAt,omitempty"`
	Counts      *Counts    `json:"counts,omitempty"`
}

// ReduceBatch assembles the batch facts from the per-repo fan-out entries in
// input order, stamping each entry's counts (reusing the single-repo fidelity
// labels) or its unavailability marker, and computing the aggregated budget. It is
// pure: the author and window are echoed (window normalized to UTC), and
// GeneratedAt is stamped by the caller. The entry list is always a non-nil slice
// so it serializes as [] rather than null.
func ReduceBatch(entries []BatchEntry, author string, since, until time.Time) BatchFacts {
	repos := make([]RepoActivity, 0, len(entries))
	for _, e := range entries {
		ra := RepoActivity{Repo: e.Repo}
		if e.Unavailable == "" {
			c := countsFrom(e.Result)
			ra.Available = true
			ra.Counts = &c
		} else {
			ra.Unavailable = e.Unavailable
			if !e.ResetAt.IsZero() {
				reset := e.ResetAt
				ra.ResetAt = &reset
			}
		}
		repos = append(repos, ra)
	}
	return BatchFacts{
		Author:    author,
		Since:     since.UTC(),
		Until:     until.UTC(),
		Repos:     repos,
		RateLimit: aggregateBudget(entries),
	}
}

// aggregateBudget reduces the per-repo budgets to the single pacing signal a
// caller should heed, mirroring the single-repo trajectory degrade: a throttle
// anywhere wins (report Remaining 0 with the earliest known reset, so a caller is
// never told it has budget while GitHub is throttling — the 0 is a throttle
// marker, not a literal primary-budget reading); otherwise the tightest successful
// budget (smallest Remaining, ties broken by the earliest reset); otherwise nil
// when no fetch carried a budget.
func aggregateBudget(entries []BatchEntry) *reduce.RateLimitFacts {
	throttled := false
	var earliestReset time.Time
	for _, e := range entries {
		if e.Unavailable != UnavailableRateLimited {
			continue
		}
		throttled = true
		if !e.ResetAt.IsZero() && (earliestReset.IsZero() || e.ResetAt.Before(earliestReset)) {
			earliestReset = e.ResetAt
		}
	}
	if throttled {
		return &reduce.RateLimitFacts{Remaining: 0, ResetAt: earliestReset}
	}

	var tightest *github.RateLimit
	for _, e := range entries {
		if e.Unavailable != "" || e.Result.RateLimit == nil {
			continue
		}
		b := e.Result.RateLimit
		if tightest == nil || b.Remaining < tightest.Remaining ||
			(b.Remaining == tightest.Remaining && b.ResetAt.Before(tightest.ResetAt)) {
			tightest = b
		}
	}
	if tightest == nil {
		return nil
	}
	return &reduce.RateLimitFacts{Remaining: tightest.Remaining, ResetAt: tightest.ResetAt}
}

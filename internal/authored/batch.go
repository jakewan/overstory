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
	// UnavailableNotAttempted marks a repo the fan-out deliberately did not fetch:
	// once one repo is throttled the batch stops launching new fetches (backpressure,
	// so it does not amplify the throttle it just hit), and every not-yet-started repo
	// is recorded as not_attempted rather than fetch_failed — it is a deliberate skip,
	// not a failure. Which repos are skipped is an arbitrary subset of those not yet
	// started, not the request-order tail.
	UnavailableNotAttempted = "not_attempted"
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

// aggregateBudget maps this batch's entries to the shared budget aggregator: a
// throttle wins (Remaining 0, earliest reset), else the tightest successful
// budget, else nil. RateLimit is carried only for an available entry, exactly
// reproducing the original skip of any unavailable or budget-less entry. The
// budget here is the GraphQL points pool.
func aggregateBudget(entries []BatchEntry) *reduce.RateLimitFacts {
	sources := make([]reduce.BudgetSource, 0, len(entries))
	for _, e := range entries {
		s := reduce.BudgetSource{
			RateLimited: e.Unavailable == UnavailableRateLimited,
			ResetAt:     e.ResetAt,
		}
		if e.Unavailable == "" {
			s.RateLimit = e.Result.RateLimit
		}
		sources = append(sources, s)
	}
	return reduce.AggregateBudget(sources)
}

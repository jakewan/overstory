package maintenance

import (
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// Per-repo unavailability reasons in a batch. The fan-out classifies each repo's
// fetch outcome into one of these; an available repo carries none. They live here
// (not in the server) so the reduction and the handler that fills BatchEntry share
// one vocabulary. Unlike the authored batch there is no author-not-found marker:
// the events stream is filtered by actor login string in the reduction, so an
// unknown actor yields zero items rather than a resolution error.
const (
	UnavailableNotFound    = "not_found"
	UnavailableRateLimited = "rate_limited"
	UnavailableFetchFailed = "fetch_failed"
	// UnavailableNotAttempted marks a repo the fan-out deliberately did not fetch:
	// once one repo is throttled the batch stops launching new fetches (backpressure,
	// so it does not amplify the throttle it just hit), and every not-yet-started repo
	// is recorded as not_attempted rather than fetch_failed — it is a deliberate skip,
	// not a failure. Which repos are skipped is an arbitrary subset of those not yet
	// started, not the request-order tail.
	UnavailableNotAttempted = "not_attempted"
)

// BatchEntry is one repo's fan-out outcome, the neutral input the server fills
// for the pure reduction: a successful Result (the repo's raw events plus its REST
// budget) when Unavailable is empty, or an Unavailable reason (with ResetAt set
// for a throttle) otherwise. The actor filter is applied per-entry by ReduceBatch,
// not here — the fetched stream is unfiltered.
type BatchEntry struct {
	Repo        string
	Result      github.IssueEventsResult
	Unavailable string
	ResetAt     time.Time
}

// BatchFacts is the batch tool's output: one batch-level identity (the actor, the
// shared window, the generation time) and a per-repo entry list, plus the
// aggregated budget. There is no cross-repo roll-up of the items — merging and the
// attention verdict stay caller-side. GeneratedAt is stamped by the handler,
// mirroring the single-repo Facts. RateLimit is the aggregated REST core-pool
// budget, omitted when no fetch carried one and none throttled.
type BatchFacts struct {
	Author      string                 `json:"author"`
	Since       time.Time              `json:"since"`
	Until       time.Time              `json:"until"`
	GeneratedAt time.Time              `json:"generatedAt"`
	Repos       []RepoActivity         `json:"repos"`
	RateLimit   *reduce.RateLimitFacts `json:"rateLimit,omitempty"`
}

// RepoActivity is one repo's slot in the batch result: either Available with its
// grouped Items (and the per-repo Truncated fidelity signal), or unavailable with
// a reason (and, for a throttle, the ResetAt the caller can retry at). Available
// is always emitted so a caller branches on it explicitly rather than inferring
// from a missing field; Items and Truncated omit on the unavailable path, and an
// available repo with no qualifying mutations simply carries no items.
type RepoActivity struct {
	Repo        string         `json:"repo"`
	Available   bool           `json:"available"`
	Unavailable string         `json:"unavailable,omitempty"`
	ResetAt     *time.Time     `json:"resetAt,omitempty"`
	Items       []ItemActivity `json:"items,omitempty"`
	Truncated   bool           `json:"truncated,omitempty"`
}

// ReduceBatch assembles the batch facts from the per-repo fan-out entries in input
// order, filtering each available entry's events to the actor's in-window
// mutations (reusing the single-repo grouping) or stamping its unavailability
// marker, and computing the aggregated budget. It is pure: the actor and window
// are echoed (window normalized to UTC), and GeneratedAt is stamped by the caller.
// The entry list is always a non-nil slice so it serializes as [] rather than null.
func ReduceBatch(entries []BatchEntry, author string, since, until time.Time) BatchFacts {
	repos := make([]RepoActivity, 0, len(entries))
	for _, e := range entries {
		ra := RepoActivity{Repo: e.Repo}
		if e.Unavailable == "" {
			ra.Available = true
			ra.Items = itemsFrom(e.Result.Events, author, since, until)
			ra.Truncated = e.Result.Truncated
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
// caller should heed, mirroring the authored batch: a throttle anywhere wins
// (report Remaining 0 with the earliest known reset, so a caller is never told it
// has budget while GitHub is throttling — the 0 is a throttle marker, not a
// literal reading); otherwise the tightest successful budget (smallest Remaining,
// ties broken by the earliest reset); otherwise nil when no fetch carried a
// budget. The budget here is the REST core pool (requests/hour), not the GraphQL
// points pool — so it must never be aggregated together with an authored batch's.
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

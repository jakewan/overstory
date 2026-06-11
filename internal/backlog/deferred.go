package backlog

import (
	"sort"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// Facts is the full backlog-review reduction: review-level identity plus
// one block per grooming signal. Repo and GeneratedAt describe the whole review,
// not any single reduction, so they live here rather than inside a block; each
// block carries its own counts and truncation seams so a caller renders them
// independently.
type Facts struct {
	Repo        string           `json:"repo"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Staleness   StalenessFacts   `json:"staleness"`
	Deferred    DeferredFacts    `json:"deferred"`
	AreaBalance AreaBalanceFacts `json:"areaBalance"`
	Quality     QualityFacts     `json:"quality"`
	RateLimit   *RateLimitFacts  `json:"rateLimit,omitempty"`
}

// RateLimitFacts is the GraphQL points-budget snapshot from the fetch, so a
// caller can pace itself: the points Remaining in the current window and the
// ResetAt instant it refills. It is absent (nil, omitted from the output) when
// the fetch carried no budget block, so a caller never mistakes an unknown
// budget for a present one.
type RateLimitFacts struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

// DeferredFacts is the compact result of the deferred-issue reduction: open
// issues bearing a maintainer-declared "deferred" label, surfaced for re-
// evaluation. Configured is true exactly when the repo declared deferred labels
// (len(labels) > 0); when false the reduction is a no-op — empty list, zero
// count — rather than an error, because "deferred" is repo-specific and has no
// generic default to guess from. OpenIssueCount stays exact even when the fetch
// window is truncated, which FetchTruncated marks.
type DeferredFacts struct {
	Configured       bool            `json:"configured"`
	ConfiguredLabels []string        `json:"configuredLabels"`
	DeferredCount    int             `json:"deferredCount"`
	FetchedCount     int             `json:"fetchedCount"`
	OpenIssueCount   int             `json:"openIssueCount"`
	DeferredIssues   []DeferredIssue `json:"deferredIssues"`
	Limit            int             `json:"limit"`
	ListTruncated    bool            `json:"listTruncated"`
	FetchTruncated   bool            `json:"fetchTruncated"`
}

// DeferredIssue is one parked open issue reduced to its identifying facts plus
// the deferred labels it matched. InactiveDays is measured from the last human
// activity; AgeDays from creation.
type DeferredIssue struct {
	Number              int       `json:"number"`
	Title               string    `json:"title"`
	URL                 string    `json:"url"`
	MatchedLabels       []string  `json:"matchedLabels"`
	InactiveDays        int       `json:"inactiveDays"`
	AgeDays             int       `json:"ageDays"`
	LastHumanActivityAt time.Time `json:"lastHumanActivityAt"`
}

// ReduceDeferred reduces the fetched open issues to deferred facts as of now: the
// issues carrying at least one configured deferred label, matched case-
// insensitively. totalOpen is the repository's exact open count, so
// OpenIssueCount stays accurate even when fewer issues were fetched; the listed
// issues are capped at listLimit, most-inactive first (ties broken by issue
// number). With no configured labels the result is a no-op — Configured false,
// empty list — since "deferred" has no generic default. now is injected so the
// reduction is deterministic.
//
// Ordering caveat: InactiveDays derives from an issue's last human activity,
// which falls back to creation time when the issue has no human comments. Parked
// issues usually have none, so for them "most-inactive first" degrades to
// "oldest-created first" — an age proxy, not time-since-parked. The truer signal
// (when the deferred label was applied) needs the issue timeline, which this
// reduction deliberately does not fetch.
func ReduceDeferred(issues []github.Issue, totalOpen int, labels []string, listLimit int, now time.Time) DeferredFacts {
	facts := DeferredFacts{
		Configured:       len(labels) > 0,
		ConfiguredLabels: append(make([]string, 0, len(labels)), labels...),
		FetchedCount:     len(issues),
		OpenIssueCount:   totalOpen,
		Limit:            listLimit,
		FetchTruncated:   len(issues) < totalOpen,
		DeferredIssues:   make([]DeferredIssue, 0),
	}
	if !facts.Configured {
		return facts
	}

	// Deferred matching is the explicit-list case of the shared matcher (no prefix
	// rules) — "deferred" is a curated subset of status labels, where a prefix
	// would over-match. The matcher returns the issue's original-cased label for an
	// explicit match, so iterating is.Labels in order and sorting reproduces the
	// prior MatchedLabels content and ordering exactly.
	matcher := newLabelMatcher(labels, nil)

	deferred := make([]DeferredIssue, 0, len(issues))
	for _, is := range issues {
		matched := make([]string, 0, len(is.Labels))
		for _, name := range is.Labels {
			if m, ok := matcher.match(name); ok {
				matched = append(matched, m)
			}
		}
		if len(matched) == 0 {
			continue
		}
		sort.Strings(matched) // deterministic order regardless of GitHub's label ordering
		deferred = append(deferred, DeferredIssue{
			Number:              is.Number,
			Title:               is.Title,
			URL:                 is.URL,
			MatchedLabels:       matched,
			InactiveDays:        daysSince(now, is.LastActivityAt),
			AgeDays:             daysSince(now, is.CreatedAt),
			LastHumanActivityAt: is.LastActivityAt,
		})
	}

	facts.DeferredCount = len(deferred) // count is not capped; only the list is

	// Most-inactive first; ties broken by issue number for deterministic output.
	sort.Slice(deferred, func(i, j int) bool {
		if deferred[i].InactiveDays != deferred[j].InactiveDays {
			return deferred[i].InactiveDays > deferred[j].InactiveDays
		}
		return deferred[i].Number < deferred[j].Number
	})

	if listLimit >= 0 && len(deferred) > listLimit {
		facts.ListTruncated = true
		deferred = deferred[:listLimit]
	}
	facts.DeferredIssues = deferred
	return facts
}

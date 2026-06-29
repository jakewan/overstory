package backlog

import (
	"sort"
	"time"

	"github.com/jakewan/overstory/internal/criticalpath"
	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// Facts is the full backlog-review reduction: review-level identity plus
// one block per grooming signal. Repo and GeneratedAt describe the whole review,
// not any single reduction, so they live here rather than inside a block; each
// block carries its own counts and truncation seams so a caller renders them
// independently.
type Facts struct {
	Repo        string    `json:"repo"`
	GeneratedAt time.Time `json:"generatedAt"`
	// The grooming-signal blocks are pointers with omitempty so block projection can
	// omit an unrequested one from the response entirely (a nil pointer drops its
	// key, unlike a value that would serialize as an empty block). A full-composite
	// response sets every pointer, so its wire bytes are identical to the pre-
	// projection value-typed shape. A requested block whose own fetch failed is still
	// non-nil — it carries its Available:false marker — so the caller can tell "asked
	// and unavailable" from "not asked".
	Staleness    *StalenessFacts          `json:"staleness,omitempty"`
	Deferred     *DeferredFacts           `json:"deferred,omitempty"`
	AreaBalance  *AreaBalanceFacts        `json:"areaBalance,omitempty"`
	Quality      *QualityFacts            `json:"quality,omitempty"`
	Overlap      *OverlapFacts            `json:"overlap,omitempty"`
	CrossRef     *CrossRefFacts           `json:"crossRef,omitempty"`
	Trajectory   *TrajectoryFacts         `json:"trajectory,omitempty"`
	PRTrajectory *PRTrajectoryFacts       `json:"prTrajectory,omitempty"`
	CriticalPath *criticalpath.Facts      `json:"criticalPath,omitempty"`
	OpenIssueSet reduce.OpenIssueSetFacts `json:"openIssueSet"`
	RateLimit    *reduce.RateLimitFacts   `json:"rateLimit,omitempty"`
	// SizeBound is set only when the response had to be trimmed to fit the
	// configured byte budget; absent (nil) on a response that fit untouched.
	SizeBound *reduce.SizeBoundFacts `json:"sizeBound,omitempty"`
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
//
// BodyRefs are the distinct #N references parsed from the issue body, ascending,
// with pull-request references and the issue's own number excluded — the parked
// issue's stated dependencies, so a client can tell whether a blocker has since
// closed. It is parsed from GitHub's rendered plaintext body (bodyText), not raw
// markdown, so only references surviving plaintext rendering appear. Non-nil even
// when empty, so it serializes as [] rather than null. It is a heuristic proxy for
// stated cross-references, complementary to the authoritative BlockedBy below.
//
// BlockedBy are the ascending, distinct numbers of the issue's still-open native
// GitHub blocked-by edges — the authoritative dependency signal for what gates this
// issue: a closed blocker is omitted (it no longer gates), and a PR can never appear
// (the edge is issue-to-issue). Unlike BodyRefs, the open/closed determination needs
// no open-issue-set resolution — the edge carries the state. Non-nil even when empty.
// BlockedByTruncated is true when the issue has more native edges than the fetch
// window read, so absence past the window is not proof the issue is unblocked.
//
// Blocking is the reverse direction: the ascending, distinct numbers of the
// still-open downstream issues this one gates — what closing it would help unblock.
// Same authoritative-edge semantics as BlockedBy, mirrored: it tells a maintainer
// how much downstream work a parked issue stands in front of, not just whether the
// parked issue is itself blocked. It is a gate this issue contributes, not
// necessarily the only one, so a downstream issue stays blocked until every issue
// blocking it closes. Non-nil even when empty; BlockingTruncated marks more native
// blocking edges than the fetch window read.
//
// SubIssues are the ascending, distinct numbers of the parked issue's still-open
// same-repository child issues — the hierarchy form of the same gate: a parent with
// open children is not startable, however quiet it looks. Same authoritative-edge
// semantics (closed children omitted, a PR can never appear, cross-repository
// children dropped). Non-nil even when empty; SubIssuesTruncated marks more native
// children than the fetch window read.
//
// SubIssuesTotal and SubIssuesCompleted are GitHub's authoritative subIssuesSummary
// counts over *all* children — every repository, never capped — so SubIssuesTotal
// minus SubIssuesCompleted is an upper bound on the open children and may exceed
// len(SubIssues) when children are cross-repository or beyond the window. That gap is
// the only signal exposing a parent gated entirely by hidden children: an empty
// SubIssues with a positive gap is a hidden gate, not readiness. It is a bound, not
// an equality (a not-planned closure can leave the gap one high), but it errs only
// toward over-reporting the gate, so it never reads a gated parent as ready.
type DeferredIssue struct {
	Number              int       `json:"number"`
	Title               string    `json:"title"`
	URL                 string    `json:"url"`
	MatchedLabels       []string  `json:"matchedLabels"`
	BodyRefs            []int     `json:"bodyRefs"`
	BlockedBy           []int     `json:"blockedBy"`
	BlockedByTruncated  bool      `json:"blockedByTruncated"`
	Blocking            []int     `json:"blocking"`
	BlockingTruncated   bool      `json:"blockingTruncated"`
	SubIssues           []int     `json:"subIssues"`
	SubIssuesTruncated  bool      `json:"subIssuesTruncated"`
	SubIssuesTotal      int       `json:"subIssuesTotal"`
	SubIssuesCompleted  int       `json:"subIssuesCompleted"`
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

	matches := deferredMatches(issues, labels)

	deferred := make([]DeferredIssue, 0, len(matches))
	for _, is := range issues {
		matched, ok := matches[is.Number]
		if !ok {
			continue
		}
		deferred = append(deferred, DeferredIssue{
			Number:              is.Number,
			Title:               is.Title,
			URL:                 is.URL,
			MatchedLabels:       matched,
			BodyRefs:            reduce.IssueRefsExcluding(is.BodyText, is.Number),
			BlockedBy:           reduce.OpenDependencyNumbers(is.BlockedBy),
			BlockedByTruncated:  is.BlockedByTruncated,
			Blocking:            reduce.OpenDependencyNumbers(is.Blocking),
			BlockingTruncated:   is.BlockingTruncated,
			SubIssues:           reduce.OpenDependencyNumbers(is.SubIssues),
			SubIssuesTruncated:  is.SubIssuesTruncated,
			SubIssuesTotal:      is.SubIssuesTotal,
			SubIssuesCompleted:  is.SubIssuesCompleted,
			InactiveDays:        reduce.DaysSince(now, is.LastActivityAt),
			AgeDays:             reduce.DaysSince(now, is.CreatedAt),
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

// deferredMatches is the single deferred-classification source for the backlog
// staleness/deferred pair: it maps each fetched issue carrying at least one
// configured deferred label to those matched labels (sorted). Deferred matching
// is the explicit-list case of the shared matcher (no prefix rules) — "deferred"
// is a curated subset of status labels, where a prefix would over-match. With no
// configured labels it returns an empty map, so callers treat "unconfigured" as
// "nothing deferred" uniformly. It ranges the full window, never a capped list.
func deferredMatches(issues []github.Issue, labels []string) map[int][]string {
	if len(labels) == 0 {
		return map[int][]string{}
	}
	matcher := reduce.NewLabelMatcher(labels, nil)
	out := make(map[int][]string, len(issues))
	for _, is := range issues {
		matched := make([]string, 0, len(is.Labels))
		for _, name := range is.Labels {
			if m, ok := matcher.Match(name); ok {
				matched = append(matched, m)
			}
		}
		if len(matched) == 0 {
			continue
		}
		sort.Strings(matched) // deterministic order regardless of GitHub's label ordering
		out[is.Number] = matched
	}
	return out
}

// DeferredNumbers returns the set of fetched issue numbers carrying at least one
// configured deferred label — the staleness reduction's exclusion set. It draws
// on the same classification ReduceDeferred uses, over the full fetched window
// (not the capped list), so staleness and deferred never disagree about which
// issues are parked.
func DeferredNumbers(issues []github.Issue, labels []string) map[int]bool {
	matches := deferredMatches(issues, labels)
	nums := make(map[int]bool, len(matches))
	for n := range matches {
		nums[n] = true
	}
	return nums
}

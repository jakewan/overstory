package summary

import (
	"sort"
	"strings"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// HygieneParams is the resolved convention a hygiene reduction runs against — the
// summary-layer mirror of the manifest blocks it reuses, kept distinct so this
// package stays decoupled from convention resolution. AreaLabels/AreaPrefixes and
// DeferredLabels are the same taxonomies the area and deferred reductions use;
// UnmilestonedAgeDays and StaleThresholdDays are day thresholds; ContextBodyLength
// is the trimmed-body length a deferred issue must reach to count as having
// context (reusing the quality body bar — 1 means "non-empty").
type HygieneParams struct {
	AreaLabels          []string
	AreaPrefixes        []reduce.PrefixRule
	DeferredLabels      []string
	UnmilestonedAgeDays int
	StaleThresholdDays  int
	ContextBodyLength   int
}

// HygieneFacts is the hygiene-signals block: the orientation signals over the
// fetched open issues, each a count plus a capped list. The signals are not
// disjoint — one issue can trip several — so the counts need not sum to anything.
// OpenIssueCount stays exact when the window truncates (FetchTruncated).
type HygieneFacts struct {
	OpenIssueCount int  `json:"openIssueCount"`
	FetchedCount   int  `json:"fetchedCount"`
	FetchTruncated bool `json:"fetchTruncated"`
	// LabelTruncatedCount is how many fetched open issues had their own label list
	// capped by the fetch (github.Issue.LabelsTruncated). It floors every label-driven
	// signal in this block, not only the area observation: an issue whose area label
	// fell in the dropped tail is counted as missing an area it actually carries, one
	// whose deferred label fell there reads as neglected rather than parked, and
	// AreaLabelsObserved can read false on a repository that does classify by area.
	// A positive value makes those readings provisional for that many issues — the
	// same axis the critical-path reduction surfaces alongside its own fetch
	// truncation, and orthogonal to FetchTruncated, which bounds which issues were
	// seen rather than how much of each was seen. Issues rarely carry enough labels
	// to reach the cap, so this is normally zero.
	LabelTruncatedCount int           `json:"labelTruncatedCount"`
	Limit               int           `json:"limit"`
	MissingArea         HygieneSignal `json:"missingArea"`
	// AreaLabelsObserved reports whether any fetched open issue carried a label
	// matching the configured area taxonomy. It qualifies MissingArea, whose count
	// alone cannot distinguish a backlog with a real labelling gap from one belonging
	// to a repository that classifies nothing by area — where the signal is fully lit
	// on every run and so carries no information.
	//
	// It is an observation over the fetched window, not a property of the repository,
	// and a false value covers several distinct situations: an area convention never
	// adopted; one adopted but not yet applied to any open issue; a real convention
	// whose labels the configured prefixes do not recognize (the generic defaults
	// cover the common area-prefixed spellings, not every scheme); an explicitly
	// emptied areaBalance; and a window holding no open issues at all, where nothing
	// was observed because there was nothing to observe. Only the caller can tell
	// these apart, which is why the signal is still reported in full rather than
	// withheld here — and why no wording built on this flag should assert that the
	// repository has no area convention.
	//
	// Two seams floor it, and both are observable. FetchTruncated marks a partial
	// window, and the issue fetch is ordered least-recently-active-first, so a
	// recently adopted convention's labels sit in exactly the tail truncation drops.
	// LabelTruncatedCount marks issues whose own label list was capped, where the area
	// label may sit in the dropped tail of an issue the window did include.
	//
	// It cannot live on HygieneParams beside the taxonomy it reads: unlike the
	// not-configured flags on the deferred and critical-path reductions, this is
	// derived from the issues rather than from the resolved manifest.
	AreaLabelsObserved     bool          `json:"areaLabelsObserved"`
	UnmilestonedAged       HygieneSignal `json:"unmilestonedAged"`
	Stale                  HygieneSignal `json:"stale"`
	DeferredWithoutContext HygieneSignal `json:"deferredWithoutContext"`
}

// HygieneSignal is one signal's full count and capped list. Count is the total
// over the fetched window (uncapped); Issues is the list capped at the limit, with
// ListTruncated set when it was capped.
type HygieneSignal struct {
	Count         int            `json:"count"`
	Issues        []HygieneIssue `json:"issues"`
	ListTruncated bool           `json:"listTruncated"`
}

// HygieneIssue is one flagged open issue reduced to its identifying facts, its
// age, and its inactivity (days since last human activity).
type HygieneIssue struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	AgeDays      int    `json:"ageDays"`
	InactiveDays int    `json:"inactiveDays"`
}

// ReduceHygiene reduces the fetched open issues to the hygiene signals as of now,
// each in a single pass. The predicates:
//   - missingArea: the issue matches no configured area.
//   - unmilestonedAged: the issue has no milestone AND its age is at or beyond
//     UnmilestonedAgeDays (a fresh unmilestoned issue is normal in-flight work).
//   - stale: inactivity (days since last human activity) is at or beyond
//     StaleThresholdDays — the same staleness bar the grooming read uses.
//   - deferredWithoutContext: the issue carries a deferred label AND its trimmed
//     body is below ContextBodyLength — parked with no explanation of why.
//
// Each signal's count is the full over-window total; its list is capped at
// listLimit, most-recently-active issues last (oldest activity first, the order a
// caller most wants for cleanup). now is injected so the reduction is
// deterministic.
func ReduceHygiene(issues []github.Issue, totalOpen int, params HygieneParams, listLimit int, now time.Time) HygieneFacts {
	facts := HygieneFacts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Limit:          listLimit,
	}
	areaMatcher := reduce.NewLabelMatcher(params.AreaLabels, params.AreaPrefixes)
	deferredMatcher := reduce.NewLabelMatcher(params.DeferredLabels, nil)

	var missingArea, unmilestonedAged, stale, deferredNoCtx []HygieneIssue
	for _, is := range issues {
		hi := HygieneIssue{
			Number:       is.Number,
			Title:        is.Title,
			URL:          is.URL,
			AgeDays:      reduce.DaysSince(now, is.CreatedAt),
			InactiveDays: reduce.DaysSince(now, is.LastActivityAt),
		}
		if is.LabelsTruncated {
			facts.LabelTruncatedCount++
		}
		deferred := deferredMatcher.MatchesAny(is.Labels)
		if !areaMatcher.MatchesAny(is.Labels) {
			missingArea = append(missingArea, hi)
		}
		if is.Milestone == nil && hi.AgeDays >= params.UnmilestonedAgeDays {
			unmilestonedAged = append(unmilestonedAged, hi)
		}
		// Staleness means neglected, not merely inactive: a deferred issue is quiet
		// by design, so it is excluded here and surfaced under the deferred signals
		// instead — keeping this orientation read consistent with the grooming read.
		if hi.InactiveDays >= params.StaleThresholdDays && !deferred {
			stale = append(stale, hi)
		}
		if deferred && len(strings.TrimSpace(is.BodyText)) < params.ContextBodyLength {
			deferredNoCtx = append(deferredNoCtx, hi)
		}
	}

	// Derived here rather than accumulated in the loop: an issue lands in missingArea
	// exactly when it matched no area, so a shorter list is itself the evidence that
	// one did. Read before toSignal so the value cannot come to depend on what that
	// function does to these slices.
	facts.AreaLabelsObserved = len(missingArea) < len(issues)

	facts.MissingArea = toSignal(missingArea, listLimit)
	facts.UnmilestonedAged = toSignal(unmilestonedAged, listLimit)
	facts.Stale = toSignal(stale, listLimit)
	facts.DeferredWithoutContext = toSignal(deferredNoCtx, listLimit)
	return facts
}

// toSignal sorts a signal's issues most-inactive first (ties by number), records
// the full count, and caps the list at listLimit, marking truncation.
func toSignal(issues []HygieneIssue, listLimit int) HygieneSignal {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].InactiveDays != issues[j].InactiveDays {
			return issues[i].InactiveDays > issues[j].InactiveDays
		}
		return issues[i].Number < issues[j].Number
	})
	sig := HygieneSignal{Count: len(issues), Issues: make([]HygieneIssue, 0, len(issues))}
	if listLimit >= 0 && len(issues) > listLimit {
		sig.ListTruncated = true
		issues = issues[:listLimit]
	}
	sig.Issues = issues
	return sig
}

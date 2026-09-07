// Package dependency holds overstory's native-dependency reduction: a pure
// function that classifies a repository's open issues by their authoritative
// GitHub blocked-by/blocking edges (and sub-issue hierarchy) into ready, blocked,
// and gate sets. Unlike the deferred reduction — which surfaces these same edges
// only for manifest-declared deferred issues — this reduction is convention-free:
// the native edges are universal, so a repo that declares no deferred convention
// still gets its authoritative dependency structure. It imports only the fetched
// shapes (github) and the shared open-edge projection (reduce).
//
// The graph-level classification is the block's reason to exist. The deferred
// (backlog) and recommendation (summary) blocks already list per-issue edges; what
// neither computes is the ready/blocked split and the gate set — the open issues
// that block others but are themselves unblocked, the highest-leverage work to do
// first. That classification is the new signal here. (The package is named for the
// edge domain it reads, like the sibling block key "dependencies"; the signal it
// derives is the gate-readiness classification.)
package dependency

import (
	"sort"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// Facts is the compact result of the dependency reduction. It carries no
// Configured field — like crossRef and overlap the reduction always applies,
// since native edges need no convention. OpenIssueCount is the repository-wide
// open total and stays exact when the fetch window truncates (FetchTruncated);
// the ready/blocked/gate classification is over the fetched window only.
//
// FetchTruncated bounds coverage but never misclassifies a fetched issue, so —
// unlike criticalPath's gate — the classification is not blanket-degraded under it.
// criticalPath's gate is absence-based ("no open critical-path issue remains in the
// stream"), which a truncated window can falsely clear, so it degrades to
// provisional. Here readiness reads each issue's own blocked-by edges, which carry
// authoritative open/closed state on the issue regardless of whether the blocker was
// fetched — a blocker outside the window still demotes its dependent out of ready.
// So the only truncation that can hide a gate is the per-issue edge cap
// (BlockedByTruncated), which the provisional class guards; FetchTruncated leaves
// the counts a floor (more issues may exist unclassified) without making any
// classified issue wrong.
//
// ReadyCount, BlockedCount, and ProvisionalCount partition the fetched open
// issues. Provisional is the truncation-safety class: an issue that presents no
// open blocker but whose blocked-by edge list was capped (BlockedByTruncated)
// cannot be confirmed ready, so it is neither counted ready nor listed as a gate —
// an empty edge list is not proof of readiness. Gates and Blocked are the two
// actionable lists (capped at Limit, counts never capped).
type Facts struct {
	OpenIssueCount   int  `json:"openIssueCount"`
	FetchedCount     int  `json:"fetchedCount"`
	FetchTruncated   bool `json:"fetchTruncated"`
	ReadyCount       int  `json:"readyCount"`
	BlockedCount     int  `json:"blockedCount"`
	ProvisionalCount int  `json:"provisionalCount"`
	// GateCount is the total number of gates before the Gates list is capped, so a
	// caller can tell how many do-first roots exist when GatesTruncated is set.
	GateCount        int     `json:"gateCount"`
	Gates            []Issue `json:"gates"`
	GatesTruncated   bool    `json:"gatesTruncated"`
	Blocked          []Issue `json:"blocked"`
	BlockedTruncated bool    `json:"blockedTruncated"`
	Limit            int     `json:"limit"`
	// Seams says which inputs the readiness verdict rests on and whether this forge
	// carries each, so a caller renders "readiness here rests on blocked-by edges
	// alone" from a stated fact rather than inferring it from a population of zeros.
	Seams SeamReport `json:"seams"`
}

// SeamReport is the forge-level state of each input a readiness verdict rests on.
// It is stated once for the whole block because a forge either carries a
// relationship or does not — unlike the per-issue states, which record what a single
// read obtained. A seam reported notApplicable here is why an issue can be ready with
// that input unexamined.
type SeamReport struct {
	BlockedBy   github.SeamState `json:"blockedBy"`
	SubIssueGap github.SeamState `json:"subIssueGap"`
}

// Issue is one open issue reduced to its identifying facts and its authoritative
// native edges. BlockedBy is what still gates it (open blocked-by edges); Blocking
// is what it still gates (open downstream). SubIssueGate is true when the
// authoritative sub-issue summary shows open children — a hidden gate the windowed
// edge lists can miss. Both edge slices are non-nil even when empty. A Gate carries
// its Blocking (the work it unblocks); a Blocked issue carries its BlockedBy (why
// it waits) — but both slices are populated on every listed issue so a caller has
// the full local structure regardless of which list the issue is in.
//
// BlockedByTruncated / BlockingTruncated mark an edge list the fetch capped, so the
// corresponding slice is a lower bound — the same per-issue honesty the deferred and
// recommendation blocks carry. It matters for a listed issue: a blocked issue's
// BlockedBy may omit further blockers, and a gate's Blocking (hence its ordering and
// the summary blockingCount) may understate how much it unblocks.
type Issue struct {
	Number             int    `json:"number"`
	Title              string `json:"title"`
	URL                string `json:"url"`
	BlockedBy          []int  `json:"blockedBy"`
	BlockedByTruncated bool   `json:"blockedByTruncated"`
	Blocking           []int  `json:"blocking"`
	BlockingTruncated  bool   `json:"blockingTruncated"`
	SubIssueGate       bool   `json:"subIssueGate"`
}

// Reduce classifies the fetched open issues by their native dependency edges.
// totalOpen keeps OpenIssueCount exact when the window is truncated; the Gates and
// Blocked lists are each capped at listLimit (counts are not). The reduction is
// time-independent, so it takes no clock.
//
// An issue is blocked when it has an open blocked-by edge or an open sub-issue gate
// (the authoritative subIssuesTotal-minus-completed gap, which witnesses open
// children even when they fall outside the window). An issue with no known gate but
// a truncated blocked-by list is provisional, not ready — the truncation contract
// that keeps a capped edge list from reading as readiness. Everything else is
// ready. A gate is a ready issue that blocks open downstream work.
func Reduce(issues []github.Issue, totalOpen int, listLimit int, caps github.Capabilities) Facts {
	facts := Facts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Gates:          make([]Issue, 0),
		Blocked:        make([]Issue, 0),
		Limit:          listLimit,
		Seams: SeamReport{
			BlockedBy:   seamForCapability(caps.BlockedByEdges),
			SubIssueGap: seamForCapability(caps.SubIssueHierarchy),
		},
	}

	for _, is := range issues {
		// The forge-level fact wins over the per-issue one: where the relationship does
		// not exist here, no read of it can have failed, so a per-issue unavailable
		// would be a contradiction rather than a second opinion.
		if !caps.BlockedByEdges {
			is.BlockedByState = github.SeamNotApplicable
		}
		if !caps.SubIssueHierarchy {
			is.SubIssueGapState = github.SeamNotApplicable
		}
		blockedBy := reduce.OpenDependencyNumbers(is.BlockedBy)
		blocking := reduce.OpenDependencyNumbers(is.Blocking)
		// The sub-issue summary counts every child (all repos, never capped), so the
		// open-child gap is authoritative even when the windowed SubIssues list is
		// empty. It is an upper bound that can read one high after a not-planned
		// closure — erring toward over-reporting the gate, never toward false-ready.
		// The gap witnesses a gate only where the forge carries the relationship at
		// all: where it does not, the zero is the absence of a concept rather than the
		// absence of children. An unreadable seam needs no test here — its zero cannot
		// clear the issue, because the provisional arm below catches it first.
		subGate := is.SubIssueGapState != github.SeamNotApplicable &&
			is.SubIssuesTotal-is.SubIssuesCompleted > 0
		// A seam the backend could not read leaves readiness unconfirmable for the
		// same reason a capped edge list does — an empty result proves nothing. A seam
		// the forge does not carry is not a fault and withholds nothing.
		unevaluable := seamWithholds(is.BlockedByState) || seamWithholds(is.SubIssueGapState)

		item := Issue{
			Number:             is.Number,
			Title:              is.Title,
			URL:                is.URL,
			BlockedBy:          blockedBy,
			BlockedByTruncated: is.BlockedByTruncated,
			Blocking:           blocking,
			BlockingTruncated:  is.BlockingTruncated,
			SubIssueGate:       subGate,
		}

		switch {
		case len(blockedBy) > 0 || subGate:
			facts.BlockedCount++
			facts.Blocked = append(facts.Blocked, item)
		case is.BlockedByTruncated || unevaluable:
			// Appears unblocked, but a capped edge list may hide an open blocker and an
			// unread seam may hide anything at all, so readiness cannot be confirmed.
			// Not ready, not a gate.
			facts.ProvisionalCount++
		default:
			facts.ReadyCount++
			if len(blocking) > 0 {
				facts.Gates = append(facts.Gates, item)
			}
		}
	}

	// Gates: most downstream work unblocked first, then by number for a total order.
	sort.Slice(facts.Gates, func(i, j int) bool {
		if len(facts.Gates[i].Blocking) != len(facts.Gates[j].Blocking) {
			return len(facts.Gates[i].Blocking) > len(facts.Gates[j].Blocking)
		}
		return facts.Gates[i].Number < facts.Gates[j].Number
	})
	// Blocked: most-gated first, then by number.
	sort.Slice(facts.Blocked, func(i, j int) bool {
		gi := len(facts.Blocked[i].BlockedBy)
		gj := len(facts.Blocked[j].BlockedBy)
		if gi != gj {
			return gi > gj
		}
		return facts.Blocked[i].Number < facts.Blocked[j].Number
	})

	// Counts are taken before the list caps, so they stay authoritative.
	facts.GateCount = len(facts.Gates)
	facts.Gates, facts.GatesTruncated = capList(facts.Gates, listLimit)
	facts.Blocked, facts.BlockedTruncated = capList(facts.Blocked, listLimit)
	return facts
}

// Classification is the summary-side projection of Facts: the ready/blocked/gate
// classification without the per-issue blocked-by/blocking edge lists and without
// the blocked list — both derivable from the recommendation block, which already
// ships every open issue's edges. It is the signal project_summary adds over
// recommendations: the graph-level split and the gate set.
type Classification struct {
	OpenIssueCount   int    `json:"openIssueCount"`
	FetchedCount     int    `json:"fetchedCount"`
	FetchTruncated   bool   `json:"fetchTruncated"`
	ReadyCount       int    `json:"readyCount"`
	BlockedCount     int    `json:"blockedCount"`
	ProvisionalCount int    `json:"provisionalCount"`
	GateCount        int    `json:"gateCount"`
	Gates            []Gate `json:"gates"`
	GatesTruncated   bool   `json:"gatesTruncated"`
	Limit            int    `json:"limit"`
	// Seams travels with the classification for the same reason it travels with the
	// facts: a ready count means something different where a seam has no carrier, and
	// a caller reading only this projection would otherwise have no way to know.
	Seams SeamReport `json:"seams"`
}

// Gate is one do-first root in the summary projection: an issue that is itself
// ready and unblocks open downstream work. BlockingCount is how many open
// downstream issues it unblocks — the classification metadata a caller ranks by,
// not the raw edge list (that lives in the recommendation block). BlockingTruncated
// marks a capped blocking-edge list, so BlockingCount (and the gate's ordering) is a
// lower bound.
type Gate struct {
	Number            int    `json:"number"`
	Title             string `json:"title"`
	BlockingCount     int    `json:"blockingCount"`
	BlockingTruncated bool   `json:"blockingTruncated"`
}

// Classification projects the full facts to the summary-side view: it keeps the
// counts and the gate set (inheriting Facts' cap and order) and drops the per-issue
// edge lists and the blocked list.
func (f Facts) Classification() Classification {
	gates := make([]Gate, 0, len(f.Gates))
	for _, g := range f.Gates {
		gates = append(gates, Gate{
			Number:            g.Number,
			Title:             g.Title,
			BlockingCount:     len(g.Blocking),
			BlockingTruncated: g.BlockingTruncated,
		})
	}
	return Classification{
		OpenIssueCount:   f.OpenIssueCount,
		FetchedCount:     f.FetchedCount,
		FetchTruncated:   f.FetchTruncated,
		ReadyCount:       f.ReadyCount,
		BlockedCount:     f.BlockedCount,
		ProvisionalCount: f.ProvisionalCount,
		GateCount:        f.GateCount,
		Gates:            gates,
		GatesTruncated:   f.GatesTruncated,
		Limit:            f.Limit,
		Seams:            f.Seams,
	}
}

// seamForCapability maps a forge's carriage of a relationship to the seam state the
// block reports for it. A carried seam reports available at block level even where an
// individual read of it failed: the per-issue state carries that, and flattening the
// two would let one unreadable issue misreport the whole forge.
func seamForCapability(carried bool) github.SeamState {
	if carried {
		return github.SeamAvailable
	}
	return github.SeamNotApplicable
}

// seamWithholds reports whether a seam state leaves a readiness verdict unconfirmed.
// It enumerates the states that leave a verdict standing rather than the states that
// withhold, so a state added later fails safe: an unrecognized one lands in
// provisional rather than falling through to ready, which is the classification this
// package exists to keep honest. The empty string is the unset zero value, which
// means available.
func seamWithholds(s github.SeamState) bool {
	switch s {
	case "", github.SeamAvailable, github.SeamNotApplicable:
		return false
	default:
		return true
	}
}

// capList caps a list at listLimit (a negative limit means uncapped), reporting
// whether it was truncated. The returned slice stays non-nil so it serializes as
// [].
func capList(list []Issue, listLimit int) ([]Issue, bool) {
	if listLimit >= 0 && len(list) > listLimit {
		return list[:listLimit], true
	}
	return list, false
}

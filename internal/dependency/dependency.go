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
// issues. Provisional is the unconfirmed-readiness class, and an empty edge list is
// not proof of readiness for either reason that lands an issue in it: the list was
// capped (BlockedByTruncated), or a seam the verdict rests on was never read
// (SeamUnavailable). Such an issue is neither counted ready nor listed as a gate.
// The two differ in blast radius rather than in kind — a capped list is per-issue and
// rare, while an unread seam usually fires across the whole window — which is why
// Seams reports the forge-level state separately rather than leaving a caller to
// infer it from the size of the count. Gates and Blocked are the two actionable lists
// (capped at Limit, counts never capped).
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

// SeamReport is the repository-level state of each input a readiness verdict rests
// on. It is stated once for the whole block because carriage is a property of the
// repository being read rather than of any one issue in it — unlike the per-issue
// states, which record what a single read obtained. A seam reported notApplicable
// here is why an issue can be ready with that input unexamined.
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
// BlockedByState and SubIssueGapState carry the same honesty one step further, for
// the same reason the truncation flags are here: a listed blocked issue's BlockedBy
// may be not merely capped but unread, and SubIssueGate reads false both where a
// parent has no open children and where the forge has no children at all. Without
// them a listed issue's edge fields would be the one place in the response where these
// distinctions are dropped.
//
// It carries no readiness field, unlike the recommendation and deferred projections of
// the same edge fields, because the lists it appears in are already partitioned by
// verdict: an issue in Gates is ready and one in Blocked is blocked, and neither list
// ever holds a provisional issue. The rule for a future projection is that one, not an
// exemption — a projection not partitioned by verdict carries the field.
type Issue struct {
	Number             int              `json:"number"`
	Title              string           `json:"title"`
	URL                string           `json:"url"`
	BlockedBy          []int            `json:"blockedBy"`
	BlockedByTruncated bool             `json:"blockedByTruncated"`
	Blocking           []int            `json:"blocking"`
	BlockingTruncated  bool             `json:"blockingTruncated"`
	SubIssueGate       bool             `json:"subIssueGate"`
	BlockedByState     github.SeamState `json:"blockedByState"`
	SubIssueGapState   github.SeamState `json:"subIssueGapState"`
}

// Reduce classifies the fetched open issues by their native dependency edges.
// totalOpen keeps OpenIssueCount exact when the window is truncated; the Gates and
// Blocked lists are each capped at listLimit (counts are not). The reduction is
// time-independent, so it takes no clock.
//
// An issue is blocked when it has an open blocked-by edge or an open sub-issue gate
// (the authoritative subIssuesTotal-minus-completed gap, which witnesses open
// children even when they fall outside the window). An issue with no known gate is
// provisional rather than ready when its blocked-by list was capped or when a seam
// the verdict rests on went unread — in both cases the emptiness is the absence of
// evidence rather than evidence of absence. Everything else is ready, and a gate is a
// ready issue that blocks open downstream work.
//
// caps says what the fetched repository carries, and it overrides the per-issue
// states: a relationship that does not exist there cannot have failed to be read, so
// an uncarried seam withholds nothing and leaves a verdict complete. Without that
// distinction the reduction would report every issue unconfirmable wherever a
// relationship is missing, which is the same signal loss as the false-ready it exists
// to prevent, in the opposite direction. It is per-repository rather than per-forge
// because a forge can gate a relationship on a repository setting.
func Reduce(issues []github.Issue, totalOpen int, listLimit int, caps github.Capabilities) Facts {
	// Idempotent, and the handler has normally applied it already so every block of a
	// response agrees; repeated here so this reduction is correct called directly.
	issues = github.ApplyCapabilities(issues, caps)
	facts := Facts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Gates:          make([]Issue, 0),
		Blocked:        make([]Issue, 0),
		Limit:          listLimit,
		Seams: SeamReport{
			BlockedBy: blockSeam(caps.CarriesBlockedByEdges(), issues,
				func(is github.Issue) github.SeamState { return is.BlockedByState }),
			SubIssueGap: blockSeam(caps.CarriesSubIssueHierarchy(), issues,
				func(is github.Issue) github.SeamState { return is.SubIssueGapState }),
		},
	}

	for _, is := range issues {
		blockedBy := reduce.OpenDependencyNumbers(is.BlockedBy)
		blocking := reduce.OpenDependencyNumbers(is.Blocking)

		item := Issue{
			Number:             is.Number,
			Title:              is.Title,
			URL:                is.URL,
			BlockedBy:          blockedBy,
			BlockedByTruncated: is.BlockedByTruncated,
			Blocking:           blocking,
			BlockingTruncated:  is.BlockingTruncated,
			// The same call the verdict below rests on, rather than a local recomputation
			// of the gap rule: this field and that verdict must never disagree.
			SubIssueGate:     reduce.SubIssueGate(is),
			BlockedByState:   is.BlockedByState,
			SubIssueGapState: is.SubIssueGapState,
		}

		switch reduce.Readiness(is) {
		case reduce.VerdictBlocked:
			facts.BlockedCount++
			facts.Blocked = append(facts.Blocked, item)
		case reduce.VerdictReady:
			facts.ReadyCount++
			// A gate is this block's own notion — a ready issue standing in front of open
			// downstream work — so it stays here rather than in the shared predicate.
			if len(blocking) > 0 {
				facts.Gates = append(facts.Gates, item)
			}
		default:
			// Provisional, and whatever a later Verdict adds: the three counts must still
			// sum to the fetched window, and an unrecognized verdict must not read as
			// ready.
			facts.ProvisionalCount++
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

// blockSeam derives the block-level state of one seam from the forge's carriage of
// the relationship and from what the fetched issues actually yielded. Carriage alone
// cannot express the case this reduction most needs to report — a relationship the
// forge has but this repository withheld from every read — so the per-issue outcome is
// folded in rather than left for a caller to infer from a provisional count.
//
// Unavailable requires *every* fetched issue to have withheld the seam, which is the
// shape a repository-wide gate produces. A mixed window stays available: one issue's
// failed read is not the repository's verdict, and the per-issue states carry it. An
// empty window stays available too — "every issue withheld it" is vacuously true over
// no issues, and reporting a fault from an empty backlog would be manufacturing one.
func blockSeam(carried bool, issues []github.Issue, state func(github.Issue) github.SeamState) github.SeamState {
	if !carried {
		return github.SeamNotApplicable
	}
	if len(issues) == 0 {
		return github.SeamAvailable
	}
	for _, is := range issues {
		if !state(is).Withholds() {
			return github.SeamAvailable
		}
	}
	return github.SeamUnavailable
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

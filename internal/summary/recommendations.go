package summary

import (
	"sort"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// RecommendationFacts is the recommendation-inputs block: the per-issue facts a
// caller ranks "what should I pick up next?" from. The reduction supplies the
// inputs and a neutral pre-ordering; the ranking judgment stays caller-side, so
// this block deliberately makes no recommendation of its own. OpenIssueCount stays
// exact when the window truncates (FetchTruncated); the candidate list is capped
// at the limit (ListTruncated).
type RecommendationFacts struct {
	OpenIssueCount int                       `json:"openIssueCount"`
	FetchedCount   int                       `json:"fetchedCount"`
	FetchTruncated bool                      `json:"fetchTruncated"`
	Limit          int                       `json:"limit"`
	ListTruncated  bool                      `json:"listTruncated"`
	Candidates     []RecommendationCandidate `json:"candidates"`
}

// RecommendationCandidate is one open issue reduced to the facts a caller ranks
// from: whether it is a bug (a configured bug label), its milestone title (nil
// when unmilestoned), its stated dependencies, its age, and its inactivity. No
// score or rank — the caller owns that.
//
// BodyRefs are the distinct #N references parsed from the issue body, ascending,
// with pull-request references and the issue's own number excluded — the issue's
// stated dependencies. A caller resolves them against the composite's open-issue-set
// block: a ref present there names a live open issue in this repo, so the caller can
// rank a candidate gated behind one after ready work; but absence is not proof of
// resolution — the ref may be a closed issue, an open PR (PRs share the number
// space), a cross-repo reference, or, on a truncated window, an open issue the fetch
// missed. It is parsed from GitHub's rendered plaintext body (bodyText), not raw
// markdown, so only references surviving plaintext rendering appear; these are a
// heuristic proxy for stated cross-references, complementary to the authoritative
// BlockedBy below. Non-nil even when empty, so it serializes as [] rather than null.
//
// BlockedBy are the ascending, distinct numbers of the candidate's still-open
// native GitHub blocked-by edges — the authoritative dependency signal a caller
// ranks readiness from. Closed blockers are omitted (they no longer gate), and a PR
// can never appear (the edge is issue-to-issue). Unlike BodyRefs, the open/closed
// state is read straight from the edge, so it needs no open-issue-set resolution and
// carries no "absence is a closed issue or PR" ambiguity. Non-nil even when empty.
// BlockedByTruncated is true when the candidate has more native edges than the fetch
// window read — there, an empty BlockedBy is not proof the candidate is ready.
//
// Blocking is the reverse direction: the ascending, distinct numbers of the
// still-open downstream issues this candidate gates — what picking it up would help
// unblock. Same authoritative-edge semantics, mirrored: it lets a caller weigh how
// much downstream work a candidate stands in front of, not just whether the
// candidate is itself ready. It is a gate this issue contributes, not necessarily
// the only one — a downstream issue several issues block stays blocked until they
// all close. Non-nil even when empty; BlockingTruncated marks more native blocking
// edges than the fetch window read.
//
// SubIssues are the ascending, distinct numbers of the candidate's still-open
// same-repository child issues — the hierarchy form of the same readiness gate: a
// parent with open children is not startable, however old or quiet it looks, so a
// caller demotes it rather than floating it to the top. Same authoritative-edge
// semantics (closed children omitted, never a PR, cross-repository children dropped).
// Non-nil even when empty; SubIssuesTruncated marks more native children than the
// fetch window read.
//
// SubIssuesTotal and SubIssuesCompleted are GitHub's authoritative subIssuesSummary
// counts over *all* children — every repository, never capped — so SubIssuesTotal
// minus SubIssuesCompleted is an upper bound on the open children and may exceed
// len(SubIssues) when children are cross-repository or beyond the window. That gap is
// what exposes a candidate gated entirely by hidden children: an empty SubIssues with
// a positive gap is a hidden gate, not readiness. It is a bound, not an equality (a
// not-planned closure can leave it one high), erring only toward over-reporting the
// gate — so it never reads a gated candidate as ready.
type RecommendationCandidate struct {
	Number             int     `json:"number"`
	Title              string  `json:"title"`
	URL                string  `json:"url"`
	IsBug              bool    `json:"isBug"`
	Milestone          *string `json:"milestone,omitempty"`
	BodyRefs           []int   `json:"bodyRefs"`
	BlockedBy          []int   `json:"blockedBy"`
	BlockedByTruncated bool    `json:"blockedByTruncated"`
	Blocking           []int   `json:"blocking"`
	BlockingTruncated  bool    `json:"blockingTruncated"`
	SubIssues          []int   `json:"subIssues"`
	SubIssuesTruncated bool    `json:"subIssuesTruncated"`
	SubIssuesTotal     int     `json:"subIssuesTotal"`
	SubIssuesCompleted int     `json:"subIssuesCompleted"`
	AgeDays            int     `json:"ageDays"`
	InactiveDays       int     `json:"inactiveDays"`
}

// ReduceRecommendations reduces the fetched open issues to ranking-input
// candidates as of now. IsBug is set by matching the issue's labels against the
// manifest's bug labels. The candidates are pre-ordered neutrally — bugs first,
// then oldest-first, then by number — so the capped list keeps the issues a caller
// is likeliest to want; this ordering is a stable pre-sort, not a recommendation,
// and the caller does the real ranking. now is injected so the reduction is
// deterministic.
func ReduceRecommendations(issues []github.Issue, totalOpen int, bugLabels []string, listLimit int, now time.Time) RecommendationFacts {
	facts := RecommendationFacts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Limit:          listLimit,
		Candidates:     make([]RecommendationCandidate, 0, len(issues)),
	}
	bugMatcher := reduce.NewLabelMatcher(bugLabels, nil)

	candidates := make([]RecommendationCandidate, 0, len(issues))
	for _, is := range issues {
		var milestone *string
		if is.Milestone != nil {
			title := is.Milestone.Title
			milestone = &title
		}
		candidates = append(candidates, RecommendationCandidate{
			Number:             is.Number,
			Title:              is.Title,
			URL:                is.URL,
			IsBug:              anyMatch(bugMatcher, is.Labels),
			Milestone:          milestone,
			BodyRefs:           reduce.IssueRefsExcluding(is.BodyText, is.Number),
			BlockedBy:          reduce.OpenDependencyNumbers(is.BlockedBy),
			BlockedByTruncated: is.BlockedByTruncated,
			Blocking:           reduce.OpenDependencyNumbers(is.Blocking),
			BlockingTruncated:  is.BlockingTruncated,
			SubIssues:          reduce.OpenDependencyNumbers(is.SubIssues),
			SubIssuesTruncated: is.SubIssuesTruncated,
			SubIssuesTotal:     is.SubIssuesTotal,
			SubIssuesCompleted: is.SubIssuesCompleted,
			AgeDays:            reduce.DaysSince(now, is.CreatedAt),
			InactiveDays:       reduce.DaysSince(now, is.LastActivityAt),
		})
	}

	// Neutral pre-sort: bugs first, then oldest-first, then number for a total
	// order. Not a ranking — just a stable order that keeps the likeliest
	// candidates when the list caps.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].IsBug != candidates[j].IsBug {
			return candidates[i].IsBug
		}
		if candidates[i].AgeDays != candidates[j].AgeDays {
			return candidates[i].AgeDays > candidates[j].AgeDays
		}
		return candidates[i].Number < candidates[j].Number
	})
	if listLimit >= 0 && len(candidates) > listLimit {
		facts.ListTruncated = true
		candidates = candidates[:listLimit]
	}
	facts.Candidates = candidates
	return facts
}

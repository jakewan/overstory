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
// stated dependencies, so a caller's "what to start next" ranking can tell a ready
// issue from one gated behind open siblings. It is parsed from GitHub's rendered
// plaintext body (bodyText), not raw markdown, so only references surviving
// plaintext rendering appear; these are a heuristic proxy for stated
// cross-references, not GitHub's native blocked-by/sub-issue edges. Non-nil even
// when empty, so it serializes as [] rather than null.
type RecommendationCandidate struct {
	Number       int     `json:"number"`
	Title        string  `json:"title"`
	URL          string  `json:"url"`
	IsBug        bool    `json:"isBug"`
	Milestone    *string `json:"milestone,omitempty"`
	BodyRefs     []int   `json:"bodyRefs"`
	AgeDays      int     `json:"ageDays"`
	InactiveDays int     `json:"inactiveDays"`
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
			Number:       is.Number,
			Title:        is.Title,
			URL:          is.URL,
			IsBug:        anyMatch(bugMatcher, is.Labels),
			Milestone:    milestone,
			BodyRefs:     reduce.IssueRefsExcluding(is.BodyText, is.Number),
			AgeDays:      reduce.DaysSince(now, is.CreatedAt),
			InactiveDays: reduce.DaysSince(now, is.LastActivityAt),
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

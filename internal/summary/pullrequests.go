package summary

import (
	"sort"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// PullRequestFacts is the open-PR state block: the in-flight work a session sees
// before picking up something new — each open PR's branch, draft/ready state, CI
// rollup, and inactivity, plus a stale-PR count. Available is false only when the
// PR fetch failed; the block then degrades rather than failing the whole summary,
// and Unavailable names the reason. OpenPRCount is the repository's exact open-PR
// total, so it stays accurate when the fetch truncates (FetchTruncated) or the
// list caps (ListTruncated).
type PullRequestFacts struct {
	Available      bool               `json:"available"`
	Unavailable    string             `json:"unavailable,omitempty"`
	OpenPRCount    int                `json:"openPRCount"`
	FetchedCount   int                `json:"fetchedCount"`
	FetchTruncated bool               `json:"fetchTruncated"`
	StalePRCount   int                `json:"stalePRCount"`
	PullRequests   []PullRequestState `json:"pullRequests"`
	Limit          int                `json:"limit"`
	ListTruncated  bool               `json:"listTruncated"`
}

// PullRequestState is one open PR reduced to the facts an orientation read needs.
// CIStatus is the head commit's check rollup (empty when none reported); Stale is
// set when InactiveDays (days since last update) reaches the PR-staleness
// threshold.
type PullRequestState struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Branch       string `json:"branch"`
	Draft        bool   `json:"draft"`
	CIStatus     string `json:"ciStatus"`
	InactiveDays int    `json:"inactiveDays"`
	Stale        bool   `json:"stale"`
}

// ReducePullRequests reduces the fetched open pull requests to the in-flight-work
// facts as of now. A PR is stale when its inactivity (days since last update) is
// at or beyond staleThresholdDays; StalePRCount is the full over-window total. The
// listed PRs are capped at listLimit, most-inactive first (ties by number).
// totalOpen keeps OpenPRCount exact when the fetch truncates. now is injected so
// the reduction is deterministic.
func ReducePullRequests(prs []github.PullRequest, totalOpen int, prFetchTruncated bool, staleThresholdDays, listLimit int, now time.Time) PullRequestFacts {
	facts := PullRequestFacts{
		Available:      true,
		OpenPRCount:    totalOpen,
		FetchedCount:   len(prs),
		FetchTruncated: prFetchTruncated,
		Limit:          listLimit,
		PullRequests:   make([]PullRequestState, 0, len(prs)),
	}

	states := make([]PullRequestState, 0, len(prs))
	for _, pr := range prs {
		inactive := reduce.DaysSince(now, pr.LastActivityAt)
		stale := inactive >= staleThresholdDays
		if stale {
			facts.StalePRCount++ // count is not capped; only the list is
		}
		states = append(states, PullRequestState{
			Number:       pr.Number,
			Title:        pr.Title,
			URL:          pr.URL,
			Branch:       pr.HeadRefName,
			Draft:        pr.IsDraft,
			CIStatus:     pr.CIStatus,
			InactiveDays: inactive,
			Stale:        stale,
		})
	}

	// Most-inactive first; ties broken by number for deterministic output.
	sort.Slice(states, func(i, j int) bool {
		if states[i].InactiveDays != states[j].InactiveDays {
			return states[i].InactiveDays > states[j].InactiveDays
		}
		return states[i].Number < states[j].Number
	})
	if listLimit >= 0 && len(states) > listLimit {
		facts.ListTruncated = true
		states = states[:listLimit]
	}
	facts.PullRequests = states
	return facts
}

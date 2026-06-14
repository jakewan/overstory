package summary

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

func mkPR(num, inactiveDays int, draft bool, ci string) github.PullRequest {
	return github.PullRequest{
		Number:         num,
		Title:          "pr",
		URL:            "u",
		IsDraft:        draft,
		HeadRefName:    "feature/x",
		CreatedAt:      ago(inactiveDays),
		LastActivityAt: ago(inactiveDays),
		CIStatus:       ci,
	}
}

// TestReducePullRequestsStaleFlagAndOrder pins the stale predicate (inactivity at
// or beyond the threshold), the uncapped stale count, and most-inactive-first
// ordering.
func TestReducePullRequestsStaleFlagAndOrder(t *testing.T) {
	prs := []github.PullRequest{
		mkPR(1, 5, false, "SUCCESS"),  // fresh (5 < 14)
		mkPR(2, 14, false, "FAILURE"), // stale at the boundary
		mkPR(3, 30, true, ""),         // stale, draft, no rollup
	}
	facts := ReducePullRequests(prs, 3, false, 14, 20, now)
	if facts.OpenPRCount != 3 || facts.StalePRCount != 2 {
		t.Errorf("OpenPRCount=%d StalePRCount=%d, want 3/2", facts.OpenPRCount, facts.StalePRCount)
	}
	// Most-inactive first: 3 (30), 2 (14), 1 (5).
	if facts.PullRequests[0].Number != 3 || facts.PullRequests[2].Number != 1 {
		t.Errorf("order = %d…%d, want 3…1 (most-inactive first)", facts.PullRequests[0].Number, facts.PullRequests[2].Number)
	}
	if facts.PullRequests[0].CIStatus != "" || !facts.PullRequests[0].Draft {
		t.Errorf("PR 3 = %+v, want draft with empty CI", facts.PullRequests[0])
	}
	if !facts.PullRequests[1].Stale || facts.PullRequests[2].Stale {
		t.Errorf("stale flags wrong: PR2 stale=%v PR1 stale=%v, want true/false", facts.PullRequests[1].Stale, facts.PullRequests[2].Stale)
	}
}

// TestReducePullRequestsExactCountAndListCap pins that OpenPRCount stays exact
// under fetch truncation and the list caps with a flag.
func TestReducePullRequestsExactCountAndListCap(t *testing.T) {
	prs := []github.PullRequest{mkPR(1, 1, false, ""), mkPR(2, 2, false, ""), mkPR(3, 3, false, "")}
	facts := ReducePullRequests(prs, 50, true, 14, 2, now)
	if facts.OpenPRCount != 50 || !facts.FetchTruncated {
		t.Errorf("OpenPRCount=%d FetchTruncated=%v, want 50/true", facts.OpenPRCount, facts.FetchTruncated)
	}
	if len(facts.PullRequests) != 2 || !facts.ListTruncated {
		t.Errorf("listed=%d truncated=%v, want 2/true", len(facts.PullRequests), facts.ListTruncated)
	}
}

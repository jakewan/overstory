package summary

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// TestReduceRecommendationsAnnotatesAndPreSorts pins the per-issue annotations
// (bug, milestone, age, inactivity) and the neutral pre-ordering — bugs first,
// then oldest — that keeps the likeliest candidates when the list caps.
func TestReduceRecommendationsAnnotatesAndPreSorts(t *testing.T) {
	issues := []github.Issue{
		mkIssue(1, 10, 2, nil, msRef(7, "Round 5")), // not a bug, milestoned
		mkIssue(2, 5, 1, []string{"bug"}, nil),      // bug, newer
		mkIssue(3, 50, 1, []string{"bug"}, nil),     // bug, older
	}
	facts := ReduceRecommendations(issues, 3, []string{"bug"}, 20, now)
	if len(facts.Candidates) != 3 {
		t.Fatalf("candidates = %d, want 3", len(facts.Candidates))
	}
	// Bugs first (3 then 2, older bug first), then the non-bug (1).
	if facts.Candidates[0].Number != 3 || facts.Candidates[1].Number != 2 || facts.Candidates[2].Number != 1 {
		t.Errorf("order = [%d %d %d], want [3 2 1] (bugs first, oldest first)",
			facts.Candidates[0].Number, facts.Candidates[1].Number, facts.Candidates[2].Number)
	}
	bug := facts.Candidates[0]
	if !bug.IsBug || bug.Milestone != nil {
		t.Errorf("candidate 3 = %+v, want IsBug true / no milestone", bug)
	}
	milestoned := facts.Candidates[2]
	if milestoned.IsBug || milestoned.Milestone == nil || *milestoned.Milestone != "Round 5" {
		t.Errorf("candidate 1 = %+v, want not-bug with milestone Round 5", milestoned)
	}
}

// TestReduceRecommendationsExactCountAndCap pins OpenIssueCount exactness under
// fetch truncation and the list cap flag.
func TestReduceRecommendationsExactCountAndCap(t *testing.T) {
	issues := []github.Issue{
		mkIssue(1, 1, 1, nil, nil), mkIssue(2, 2, 1, nil, nil), mkIssue(3, 3, 1, nil, nil),
	}
	facts := ReduceRecommendations(issues, 90, nil, 2, now)
	if facts.OpenIssueCount != 90 || !facts.FetchTruncated {
		t.Errorf("OpenIssueCount=%d FetchTruncated=%v, want 90/true", facts.OpenIssueCount, facts.FetchTruncated)
	}
	if len(facts.Candidates) != 2 || !facts.ListTruncated {
		t.Errorf("listed=%d truncated=%v, want 2/true", len(facts.Candidates), facts.ListTruncated)
	}
}

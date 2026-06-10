package backlog

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

func labeledIssue(num, inactiveDays int, labels ...string) github.Issue {
	is := issueInactive(num, inactiveDays)
	is.Labels = labels
	return is
}

func TestReduceDeferredMatchesConfiguredLabels(t *testing.T) {
	issues := []github.Issue{
		labeledIssue(1, 50, "blocked"),
		labeledIssue(2, 50, "bug"), // not a deferred label
		labeledIssue(3, 50, "deferred"),
	}
	facts := ReduceDeferred(issues, 3, []string{"deferred", "blocked"}, 20, now)
	if !facts.Configured {
		t.Error("Configured = false, want true")
	}
	if facts.DeferredCount != 2 {
		t.Errorf("DeferredCount = %d, want 2", facts.DeferredCount)
	}
	if len(facts.DeferredIssues) != 2 {
		t.Fatalf("listed %d, want 2", len(facts.DeferredIssues))
	}
}

func TestReduceDeferredCaseInsensitive(t *testing.T) {
	// Configured "deferred" matches an issue labeled "DEFERRED"; the matched
	// label echoes the issue's original casing.
	issues := []github.Issue{labeledIssue(1, 50, "DEFERRED")}
	facts := ReduceDeferred(issues, 1, []string{"deferred"}, 20, now)
	if facts.DeferredCount != 1 {
		t.Fatalf("DeferredCount = %d, want 1 (case-insensitive)", facts.DeferredCount)
	}
	if got := facts.DeferredIssues[0].MatchedLabels; len(got) != 1 || got[0] != "DEFERRED" {
		t.Errorf("MatchedLabels = %v, want [DEFERRED] (original casing echoed)", got)
	}
}

func TestReduceDeferredMultipleLabelsOnOneIssue(t *testing.T) {
	// An issue carrying several deferred labels appears once, with all matches
	// listed in deterministic (sorted) order.
	issues := []github.Issue{labeledIssue(1, 50, "blocked", "bug", "deferred")}
	facts := ReduceDeferred(issues, 1, []string{"deferred", "blocked"}, 20, now)
	if facts.DeferredCount != 1 {
		t.Fatalf("DeferredCount = %d, want 1 (one issue, multiple matches)", facts.DeferredCount)
	}
	got := facts.DeferredIssues[0].MatchedLabels
	if len(got) != 2 || got[0] != "blocked" || got[1] != "deferred" {
		t.Errorf("MatchedLabels = %v, want [blocked deferred] (sorted, 'bug' excluded)", got)
	}
}

func TestReduceDeferredNotConfigured(t *testing.T) {
	// With no configured labels the reduction is a no-op, but the window counts
	// are still populated for a stable shape — and the slices are non-nil so they
	// serialize as [] rather than null.
	issues := []github.Issue{labeledIssue(1, 50, "blocked")}
	facts := ReduceDeferred(issues, 500, nil, 20, now)
	if facts.Configured {
		t.Error("Configured = true, want false (no labels)")
	}
	if facts.DeferredCount != 0 || len(facts.DeferredIssues) != 0 {
		t.Errorf("DeferredCount=%d list=%d, want 0/0", facts.DeferredCount, len(facts.DeferredIssues))
	}
	if facts.FetchedCount != 1 {
		t.Errorf("FetchedCount = %d, want 1 (counts populated even when not configured)", facts.FetchedCount)
	}
	if facts.OpenIssueCount != 500 || !facts.FetchTruncated {
		t.Errorf("OpenIssueCount=%d FetchTruncated=%v, want 500/true", facts.OpenIssueCount, facts.FetchTruncated)
	}
	if facts.DeferredIssues == nil || facts.ConfiguredLabels == nil {
		t.Error("DeferredIssues/ConfiguredLabels are nil; want non-nil empty slices (serialize as [])")
	}
}

func TestReduceDeferredListTruncationAndOrder(t *testing.T) {
	// Most-inactive first; the list is capped at the limit while the count is not.
	issues := []github.Issue{
		labeledIssue(1, 40, "deferred"),
		labeledIssue(2, 100, "deferred"),
		labeledIssue(3, 70, "deferred"),
	}
	facts := ReduceDeferred(issues, 3, []string{"deferred"}, 2, now)
	if facts.DeferredCount != 3 {
		t.Errorf("DeferredCount = %d, want 3 (count not capped)", facts.DeferredCount)
	}
	if !facts.ListTruncated {
		t.Error("ListTruncated = false, want true (3 deferred, limit 2)")
	}
	if len(facts.DeferredIssues) != 2 {
		t.Fatalf("listed %d, want 2", len(facts.DeferredIssues))
	}
	if facts.DeferredIssues[0].Number != 2 || facts.DeferredIssues[1].Number != 3 {
		t.Errorf("order = [%d,%d], want [2,3] (most-inactive first)",
			facts.DeferredIssues[0].Number, facts.DeferredIssues[1].Number)
	}
}

func TestReduceDeferredTieBreakByNumber(t *testing.T) {
	issues := []github.Issue{
		labeledIssue(5, 50, "deferred"),
		labeledIssue(2, 50, "deferred"),
	}
	facts := ReduceDeferred(issues, 2, []string{"deferred"}, 20, now)
	if facts.DeferredIssues[0].Number != 2 {
		t.Errorf("tie not broken by number ascending: got %d first, want 2", facts.DeferredIssues[0].Number)
	}
}

func TestReduceDeferredExactOpenCountAndFetchTruncation(t *testing.T) {
	issues := []github.Issue{labeledIssue(1, 100, "deferred"), labeledIssue(2, 90, "deferred")}
	facts := ReduceDeferred(issues, 500, []string{"deferred"}, 20, now)
	if facts.OpenIssueCount != 500 {
		t.Errorf("OpenIssueCount = %d, want 500 (exact, from totalOpen)", facts.OpenIssueCount)
	}
	if facts.FetchedCount != 2 {
		t.Errorf("FetchedCount = %d, want 2", facts.FetchedCount)
	}
	if !facts.FetchTruncated {
		t.Error("FetchTruncated = false, want true (2 fetched of 500)")
	}
}

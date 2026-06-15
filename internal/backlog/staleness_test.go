package backlog

import (
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

var now = time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)

func ago(days int) time.Time { return now.AddDate(0, 0, -days) }

func issueInactive(num, inactiveDays int) github.Issue {
	return github.Issue{
		Number:         num,
		Title:          "t",
		URL:            "u",
		CreatedAt:      ago(400),
		LastActivityAt: ago(inactiveDays),
	}
}

func TestReduceStalenessThresholdBoundary(t *testing.T) {
	// At exactly the threshold an issue is stale; one day fresher is not.
	issues := []github.Issue{
		issueInactive(1, 30), // stale (>= 30)
		issueInactive(2, 29), // fresh
	}
	facts := ReduceStaleness(issues, 2, 30, 20, nil, now)
	if facts.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1", facts.StaleCount)
	}
	if facts.FreshCount != 1 {
		t.Errorf("FreshCount = %d, want 1", facts.FreshCount)
	}
}

func TestReduceStalenessExcludesDeferred(t *testing.T) {
	// A deferred issue is neither stale nor fresh: it leaves the staleness
	// universe entirely, even when its inactivity is well past the threshold.
	// The remaining issues partition cleanly into stale and fresh, and the three
	// counts sum to the fetched total.
	issues := []github.Issue{
		issueInactive(1, 100), // deferred + inactive → excluded
		issueInactive(2, 50),  // plain stale
		issueInactive(3, 5),   // deferred + active → excluded
		issueInactive(4, 5),   // plain fresh
	}
	deferred := map[int]bool{1: true, 3: true}
	facts := ReduceStaleness(issues, 4, 30, 20, deferred, now)

	if facts.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1", facts.StaleCount)
	}
	if facts.FreshCount != 1 {
		t.Errorf("FreshCount = %d, want 1", facts.FreshCount)
	}
	if facts.DeferredExcludedCount != 2 {
		t.Errorf("DeferredExcludedCount = %d, want 2", facts.DeferredExcludedCount)
	}
	if got := facts.StaleCount + facts.FreshCount + facts.DeferredExcludedCount; got != facts.FetchedCount {
		t.Errorf("partition broken: sum = %d, want fetchedCount %d", got, facts.FetchedCount)
	}
	// Deferred issues appear in neither the list nor the buckets.
	for _, si := range facts.StaleIssues {
		if si.Number == 1 || si.Number == 3 {
			t.Errorf("deferred issue #%d leaked into staleIssues", si.Number)
		}
	}
	total := 0
	for _, b := range facts.Buckets {
		total += b.Count
	}
	if total != facts.StaleCount {
		t.Errorf("bucket counts sum to %d, want StaleCount %d (deferred must not enter buckets)", total, facts.StaleCount)
	}
}

func TestReduceStalenessClampsFutureActivity(t *testing.T) {
	// A future LastActivityAt (clock skew) must not produce a negative inactive
	// count nor read as stale.
	future := github.Issue{Number: 1, CreatedAt: ago(10), LastActivityAt: now.AddDate(0, 0, 5)}
	facts := ReduceStaleness([]github.Issue{future}, 1, 30, 20, nil, now)
	if facts.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0 (future activity is not stale)", facts.StaleCount)
	}
}

func TestReduceStalenessExactOpenCountAndFetchTruncation(t *testing.T) {
	issues := []github.Issue{issueInactive(1, 100), issueInactive(2, 90)}
	facts := ReduceStaleness(issues, 500, 30, 20, nil, now)
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

func TestReduceStalenessListTruncationAndOrder(t *testing.T) {
	// Most-stale first; the list is capped at the limit while the count is not.
	issues := []github.Issue{
		issueInactive(1, 40),
		issueInactive(2, 100),
		issueInactive(3, 70),
	}
	facts := ReduceStaleness(issues, 3, 30, 2, nil, now)
	if facts.StaleCount != 3 {
		t.Errorf("StaleCount = %d, want 3", facts.StaleCount)
	}
	if !facts.ListTruncated {
		t.Error("ListTruncated = false, want true (3 stale, limit 2)")
	}
	if len(facts.StaleIssues) != 2 {
		t.Fatalf("listed %d, want 2", len(facts.StaleIssues))
	}
	if facts.StaleIssues[0].Number != 2 || facts.StaleIssues[1].Number != 3 {
		t.Errorf("order = [%d,%d], want [2,3] (most-stale first)", facts.StaleIssues[0].Number, facts.StaleIssues[1].Number)
	}
}

func TestReduceStalenessTieBreakByNumber(t *testing.T) {
	issues := []github.Issue{issueInactive(5, 50), issueInactive(2, 50)}
	facts := ReduceStaleness(issues, 2, 30, 20, nil, now)
	if facts.StaleIssues[0].Number != 2 {
		t.Errorf("tie not broken by number ascending: got %d first, want 2", facts.StaleIssues[0].Number)
	}
}

func TestReduceStalenessBuckets(t *testing.T) {
	// threshold 30: bands [30,60), [60,90), [90,inf).
	issues := []github.Issue{
		issueInactive(1, 45),  // band 0
		issueInactive(2, 59),  // band 0
		issueInactive(3, 75),  // band 1
		issueInactive(4, 200), // band 2
	}
	facts := ReduceStaleness(issues, 4, 30, 20, nil, now)
	want := []int{2, 1, 1}
	if len(facts.Buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(facts.Buckets))
	}
	for i, w := range want {
		if facts.Buckets[i].Count != w {
			t.Errorf("bucket %d count = %d, want %d", i, facts.Buckets[i].Count, w)
		}
	}
	if facts.Buckets[2].MaxDays != 0 {
		t.Errorf("open-ended band MaxDays = %d, want 0", facts.Buckets[2].MaxDays)
	}
}

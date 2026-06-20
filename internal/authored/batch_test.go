package authored

import (
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

func batchWindow() (time.Time, time.Time) {
	return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
}

// TestCountsFromStampsFidelity pins the shared helper Reduce and ReduceBatch both
// use: each count maps through and carries its per-category fidelity label.
func TestCountsFromStampsFidelity(t *testing.T) {
	c := countsFrom(github.AuthoredActivityResult{
		CommitsAuthored: 12, IssuesOpened: 3, PullRequestsOpened: 5,
		ReviewsSubmitted: 7, PullRequestsEngaged: 9, IssuesEngaged: 4,
	})
	for _, tc := range []struct {
		name string
		got  Count
		want int
	}{
		{"commitsAuthored", c.CommitsAuthored, 12},
		{"issuesOpened", c.IssuesOpened, 3},
		{"pullRequestsOpened", c.PullRequestsOpened, 5},
		{"reviewsSubmitted", c.ReviewsSubmitted, 7},
		{"pullRequestsEngaged", c.PullRequestsEngaged, 9},
		{"issuesEngaged", c.IssuesEngaged, 4},
	} {
		if tc.got.Count != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got.Count, tc.want)
		}
		if tc.got.Fidelity == "" {
			t.Errorf("%s carries no fidelity label", tc.name)
		}
	}
}

// TestReduceBatchMapsEntriesAndTightestBudget pins the core reduction: entries
// map through in order with per-entry Repo, available entries carry counts and
// unavailable ones carry their marker, and the aggregated budget is the tightest
// (smallest Remaining) across the successful repos.
func TestReduceBatchMapsEntriesAndTightestBudget(t *testing.T) {
	since, until := batchWindow()
	r1 := until.Add(time.Hour)
	r2 := until.Add(30 * time.Minute)
	facts := ReduceBatch([]BatchEntry{
		{Repo: "acme/widgets", Result: withRL(4500, r1, 12)},
		{Repo: "acme/gadgets", Result: withRL(100, r2, 1)}, // tighter
		{Repo: "ghost/missing", Unavailable: UnavailableNotFound},
	}, "alice", since, until)

	if len(facts.Repos) != 3 {
		t.Fatalf("len(Repos) = %d, want 3", len(facts.Repos))
	}
	if facts.Repos[0].Repo != "acme/widgets" || !facts.Repos[0].Available || facts.Repos[0].Counts == nil {
		t.Errorf("Repos[0] = %+v, want acme/widgets available with counts", facts.Repos[0])
	}
	if facts.Repos[0].Counts.CommitsAuthored.Count != 12 || facts.Repos[0].Counts.CommitsAuthored.Fidelity == "" {
		t.Errorf("Repos[0] counts = %+v, want commits 12 with a fidelity label", facts.Repos[0].Counts)
	}
	if nf := facts.Repos[2]; nf.Available || nf.Counts != nil || nf.Unavailable != UnavailableNotFound {
		t.Errorf("Repos[2] = %+v, want unavailable not_found with no counts", nf)
	}
	if facts.RateLimit == nil || facts.RateLimit.Remaining != 100 || !facts.RateLimit.ResetAt.Equal(r2) {
		t.Errorf("RateLimit = %+v, want tightest {100, %v}", facts.RateLimit, r2)
	}
	if !facts.Since.Equal(since) || !facts.Until.Equal(until) || facts.Author != "alice" {
		t.Errorf("identity = (%v,%v,%q), want (%v,%v,alice)", facts.Since, facts.Until, facts.Author, since, until)
	}
}

// TestReduceBatchThrottleOverridesBudget pins that any throttled entry wins the
// budget aggregation with {0, earliest reset}, even when a healthy repo reports a
// generous Remaining — a caller must not be told it has budget mid-throttle.
func TestReduceBatchThrottleOverridesBudget(t *testing.T) {
	since, until := batchWindow()
	early := until.Add(10 * time.Minute)
	late := until.Add(time.Hour)
	facts := ReduceBatch([]BatchEntry{
		{Repo: "acme/healthy", Result: withRL(5000, until.Add(2*time.Hour), 1)},
		{Repo: "acme/late", Unavailable: UnavailableRateLimited, ResetAt: late},
		{Repo: "acme/early", Unavailable: UnavailableRateLimited, ResetAt: early},
	}, "alice", since, until)

	if facts.RateLimit == nil || facts.RateLimit.Remaining != 0 {
		t.Fatalf("RateLimit = %+v, want Remaining 0 (throttle wins)", facts.RateLimit)
	}
	if !facts.RateLimit.ResetAt.Equal(early) {
		t.Errorf("ResetAt = %v, want the earliest throttle reset %v", facts.RateLimit.ResetAt, early)
	}
	// The throttled entries carry their own reset; the healthy one stays available.
	if facts.Repos[1].ResetAt == nil || !facts.Repos[1].ResetAt.Equal(late) {
		t.Errorf("Repos[1].ResetAt = %v, want %v", facts.Repos[1].ResetAt, late)
	}
	if !facts.Repos[0].Available {
		t.Error("the healthy repo must remain available")
	}
}

// TestReduceBatchOmitsBudgetWhenNoneObserved pins that a batch where no fetch
// carried a budget (and none throttled) reports a nil RateLimit, so the field
// omits rather than rendering a present-but-zero budget.
func TestReduceBatchOmitsBudgetWhenNoneObserved(t *testing.T) {
	since, until := batchWindow()
	facts := ReduceBatch([]BatchEntry{
		{Repo: "acme/a", Result: github.AuthoredActivityResult{CommitsAuthored: 1}},
		{Repo: "acme/b", Unavailable: UnavailableFetchFailed},
	}, "alice", since, until)
	if facts.RateLimit != nil {
		t.Errorf("RateLimit = %+v, want nil (no budget observed)", facts.RateLimit)
	}
}

// TestReduceBatchAllUnavailableStillWellFormed pins that a batch where every repo
// failed still returns a non-nil entry slice (serializes as []) with each marker.
func TestReduceBatchAllUnavailableStillWellFormed(t *testing.T) {
	since, until := batchWindow()
	facts := ReduceBatch([]BatchEntry{
		{Repo: "ghost/a", Unavailable: UnavailableNotFound},
		{Repo: "ghost/b", Unavailable: UnavailableFetchFailed},
	}, "alice", since, until)
	if facts.Repos == nil {
		t.Fatal("Repos = nil, want a non-nil slice that serializes as []")
	}
	if len(facts.Repos) != 2 || facts.Repos[0].Available || facts.Repos[1].Available {
		t.Errorf("Repos = %+v, want two unavailable entries", facts.Repos)
	}
}

func withRL(remaining int, reset time.Time, commits int) github.AuthoredActivityResult {
	return github.AuthoredActivityResult{
		CommitsAuthored: commits,
		RateLimit:       &github.RateLimit{Remaining: remaining, ResetAt: reset},
	}
}

package maintenance

import (
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

func eventsResult(events []github.IssueEvent, remaining int, reset time.Time) github.IssueEventsResult {
	return github.IssueEventsResult{Events: events, RateLimit: &github.RateLimit{Remaining: remaining, ResetAt: reset}}
}

// TestReduceBatchPreservesOrderAndGroupsPerRepo pins the core batch contract:
// entries reduce in input order, an available repo carries its grouped items
// (filtered to the actor and window like the single-repo reduction), a failing
// repo degrades to its marker rather than sinking the others, and the window and
// actor are echoed once.
func TestReduceBatchPreservesOrderAndGroupsPerRepo(t *testing.T) {
	since := base
	until := base.Add(time.Hour)
	entries := []BatchEntry{
		{Repo: "acme/a", Result: eventsResult([]github.IssueEvent{
			ev(1, "labeled", "alice", 5, 100, false),
			ev(2, "labeled", "other", 6, 100, false), // dropped: wrong actor
		}, 4000, until)},
		{Repo: "acme/b", Unavailable: UnavailableNotFound},
		{Repo: "acme/c", Result: eventsResult(nil, 4900, until)}, // available, no items
	}
	facts := ReduceBatch(entries, "alice", since, until)

	if len(facts.Repos) != 3 {
		t.Fatalf("len(Repos) = %d, want 3", len(facts.Repos))
	}
	want := []string{"acme/a", "acme/b", "acme/c"}
	for i, w := range want {
		if facts.Repos[i].Repo != w {
			t.Errorf("Repos[%d].Repo = %q, want %q", i, facts.Repos[i].Repo, w)
		}
	}
	a := facts.Repos[0]
	if !a.Available || len(a.Items) != 1 || a.Items[0].Number != 100 || len(a.Items[0].Events) != 1 {
		t.Errorf("Repos[0] = %+v, want available with one item, one event", a)
	}
	b := facts.Repos[1]
	if b.Available || b.Unavailable != UnavailableNotFound {
		t.Errorf("Repos[1] = %+v, want unavailable not_found", b)
	}
	c := facts.Repos[2]
	if !c.Available || len(c.Items) != 0 {
		t.Errorf("Repos[2] = %+v, want available with no items", c)
	}
	if facts.Author != "alice" {
		t.Errorf("Author = %q, want alice", facts.Author)
	}
}

// TestReduceBatchAggregatesTightestBudget pins that with no throttle the batch
// reports the tightest successful budget (smallest remaining), so a caller paces
// on the most-constrained repo.
func TestReduceBatchAggregatesTightestBudget(t *testing.T) {
	r1 := base.Add(time.Hour)
	r2 := base.Add(30 * time.Minute)
	entries := []BatchEntry{
		{Repo: "acme/a", Result: eventsResult(nil, 4500, r1)},
		{Repo: "acme/b", Result: eventsResult(nil, 100, r2)}, // tighter
	}
	facts := ReduceBatch(entries, "a", base, base.Add(time.Hour))
	if facts.RateLimit == nil {
		t.Fatal("RateLimit = nil, want the tightest budget")
	}
	if facts.RateLimit.Remaining != 100 || !facts.RateLimit.ResetAt.Equal(r2) {
		t.Errorf("RateLimit = %+v, want {Remaining:100, ResetAt:%v}", facts.RateLimit, r2)
	}
}

// TestReduceBatchThrottleOverridesBudget pins the throttle-wins rule: a throttled
// repo forces Remaining 0 with the earliest reset, even when a healthy repo
// reports a comfortable budget — a caller is never told it has budget mid-throttle.
func TestReduceBatchThrottleOverridesBudget(t *testing.T) {
	reset := base.Add(15 * time.Minute)
	entries := []BatchEntry{
		{Repo: "acme/a", Unavailable: UnavailableRateLimited, ResetAt: reset},
		{Repo: "acme/b", Result: eventsResult(nil, 5000, base.Add(time.Hour))},
	}
	facts := ReduceBatch(entries, "a", base, base.Add(time.Hour))
	if facts.RateLimit == nil || facts.RateLimit.Remaining != 0 || !facts.RateLimit.ResetAt.Equal(reset) {
		t.Errorf("RateLimit = %+v, want {Remaining:0, ResetAt:%v}", facts.RateLimit, reset)
	}
	if facts.Repos[0].ResetAt == nil || !facts.Repos[0].ResetAt.Equal(reset) {
		t.Errorf("Repos[0].ResetAt = %v, want %v", facts.Repos[0].ResetAt, reset)
	}
}

// TestReduceBatchReposNeverNil pins that the per-repo slice is non-nil even for an
// empty batch, so it serializes as [].
func TestReduceBatchReposNeverNil(t *testing.T) {
	facts := ReduceBatch(nil, "a", base, base.Add(time.Hour))
	if facts.Repos == nil {
		t.Error("Repos = nil, want a non-nil empty slice")
	}
}

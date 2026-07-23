package maintenance

import (
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

var base = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// ev builds an issue event at base+offset minutes, with a monotonic id matching
// the offset so id order and time order agree by default; payload fields are set
// by the caller after construction where a type needs them.
func ev(id int64, typ, actor string, offsetMin int, num int, isPR bool) github.IssueEvent {
	return github.IssueEvent{
		EventID:     id,
		Type:        typ,
		Actor:       actor,
		CreatedAt:   base.Add(time.Duration(offsetMin) * time.Minute),
		IssueNumber: num,
		IssueTitle:  "item",
		IssueIsPR:   isPR,
	}
}

// TestReduceFiltersByActorEventSetAndWindow pins the three independent filters: an
// event survives only when its actor matches (case-insensitively), its type is in
// the mutation set, and its instant is within [since, until].
func TestReduceFiltersByActorEventSetAndWindow(t *testing.T) {
	since := base
	until := base.Add(time.Hour)
	events := []github.IssueEvent{
		ev(1, "labeled", "octocat", 10, 100, false),     // kept
		ev(2, "labeled", "someoneelse", 11, 100, false), // dropped: wrong actor
		ev(3, "subscribed", "octocat", 12, 100, false),  // dropped: not a mutation
		ev(4, "labeled", "octocat", -5, 101, false),     // dropped: before since
		ev(5, "labeled", "octocat", 90, 102, false),     // dropped: after until
		ev(6, "closed", "OctoCat", 20, 100, false),      // kept: actor case-folds
	}
	facts := Reduce(github.IssueEventsResult{Events: events}, "octocat", since, until)

	if len(facts.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1 (only issue 100 survives)", len(facts.Items))
	}
	if facts.Items[0].Number != 100 {
		t.Fatalf("Items[0].Number = %d, want 100", facts.Items[0].Number)
	}
	if got := len(facts.Items[0].Events); got != 2 {
		t.Fatalf("issue 100 has %d events, want 2 (labeled + closed)", got)
	}
	// Window bounds are echoed normalized to UTC.
	if !facts.Since.Equal(since) || !facts.Until.Equal(until) {
		t.Errorf("window = [%v,%v], want [%v,%v]", facts.Since, facts.Until, since, until)
	}
	if facts.Author != "octocat" {
		t.Errorf("Author = %q, want octocat", facts.Author)
	}
}

// TestReduceWindowBoundsInclusive pins that an event exactly on either bound is
// kept — the filter is since <= at <= until, not a half-open interval.
func TestReduceWindowBoundsInclusive(t *testing.T) {
	since := base
	until := base.Add(time.Hour)
	onSince := github.IssueEvent{EventID: 1, Type: "labeled", Actor: "a", CreatedAt: since, IssueNumber: 1, IssueTitle: "x"}
	onUntil := github.IssueEvent{EventID: 2, Type: "labeled", Actor: "a", CreatedAt: until, IssueNumber: 2, IssueTitle: "y"}
	facts := Reduce(github.IssueEventsResult{Events: []github.IssueEvent{onSince, onUntil}}, "a", since, until)
	if len(facts.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2 (both bounds inclusive)", len(facts.Items))
	}
}

// TestReduceGroupsAndOrders pins grouping and both ordering axes: events group by
// item, items are ordered most-recently-touched first, and events within an item
// are ordered chronologically (oldest first) by event id.
func TestReduceGroupsAndOrders(t *testing.T) {
	since := base
	until := base.Add(2 * time.Hour)
	events := []github.IssueEvent{
		ev(10, "labeled", "a", 5, 100, false),  // issue 100, older
		ev(40, "closed", "a", 40, 100, false),  // issue 100, newest overall
		ev(20, "labeled", "a", 20, 200, false), // issue 200
		ev(15, "milestoned", "a", 15, 200, false),
	}
	facts := Reduce(github.IssueEventsResult{Events: events}, "a", since, until)

	if len(facts.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(facts.Items))
	}
	// Item order: 100 first (its newest event id 40 > 200's newest id 20).
	if facts.Items[0].Number != 100 || facts.Items[1].Number != 200 {
		t.Fatalf("item order = [%d,%d], want [100,200]", facts.Items[0].Number, facts.Items[1].Number)
	}
	// Within item 100: labeled (id 10) before closed (id 40).
	e100 := facts.Items[0].Events
	if len(e100) != 2 || e100[0].Type != "labeled" || e100[1].Type != "closed" {
		t.Fatalf("issue 100 events = %+v, want labeled then closed", e100)
	}
	// Within item 200: milestoned (id 15) before labeled (id 20) — id order, not
	// stream order.
	e200 := facts.Items[1].Events
	if len(e200) != 2 || e200[0].Type != "milestoned" || e200[1].Type != "labeled" {
		t.Fatalf("issue 200 events = %+v, want milestoned then labeled", e200)
	}
}

// TestReducePopulatesPayloadFields pins that each event type's payload survives
// the projection and that payload-less types omit cleanly.
func TestReducePopulatesPayloadFields(t *testing.T) {
	since := base
	until := base.Add(time.Hour)
	labeled := ev(1, "labeled", "a", 1, 1, false)
	labeled.Label = "reductions"
	milestoned := ev(2, "milestoned", "a", 2, 2, false)
	milestoned.Milestone = "Round 6"
	assigned := ev(3, "assigned", "a", 3, 3, false)
	assigned.Assignee = "a"
	renamed := ev(4, "renamed", "a", 4, 4, false)
	renamed.RenameFrom = "old title"
	renamed.RenameTo = "new title"
	renamed.ViaAutomation = true
	closed := ev(5, "closed", "a", 5, 5, false)

	facts := Reduce(github.IssueEventsResult{Events: []github.IssueEvent{labeled, milestoned, assigned, renamed, closed}}, "a", since, until)
	byNum := map[int]Event{}
	for _, it := range facts.Items {
		byNum[it.Number] = it.Events[0]
	}

	if byNum[1].Label != "reductions" {
		t.Errorf("labeled.Label = %q, want reductions", byNum[1].Label)
	}
	if byNum[2].Milestone != "Round 6" {
		t.Errorf("milestoned.Milestone = %q, want Round 6", byNum[2].Milestone)
	}
	if byNum[3].Assignee != "a" {
		t.Errorf("assigned.Assignee = %q, want a", byNum[3].Assignee)
	}
	if byNum[4].RenameFrom != "old title" || byNum[4].RenameTo != "new title" {
		t.Errorf("renamed = %+v, want from/to set", byNum[4])
	}
	if !byNum[4].ViaAutomation {
		t.Error("renamed.ViaAutomation = false, want true")
	}
	// A closed event carries no payload.
	if c := byNum[5]; c.Label != "" || c.Milestone != "" || c.Assignee != "" || c.RenameFrom != "" || c.RenameTo != "" {
		t.Errorf("closed event carries payload %+v, want all empty", c)
	}
}

// TestReducePreservesPullRequestFlag pins that the issue/PR distinction travels to
// the item so a caller can split the mix.
func TestReducePreservesPullRequestFlag(t *testing.T) {
	since := base
	until := base.Add(time.Hour)
	events := []github.IssueEvent{
		ev(1, "labeled", "a", 1, 100, false),
		ev(2, "labeled", "a", 2, 200, true),
	}
	facts := Reduce(github.IssueEventsResult{Events: events}, "a", since, until)
	byNum := map[int]ItemActivity{}
	for _, it := range facts.Items {
		byNum[it.Number] = it
	}
	if byNum[100].IsPullRequest {
		t.Error("issue 100 IsPullRequest = true, want false")
	}
	if !byNum[200].IsPullRequest {
		t.Error("PR 200 IsPullRequest = false, want true")
	}
}

// TestReduceItemsNeverNil pins the non-null convention: with no qualifying events
// Items is an empty, non-nil slice (serializes as []), and Truncated passes
// through from the fetch result.
func TestReduceItemsNeverNil(t *testing.T) {
	facts := Reduce(github.IssueEventsResult{Truncated: true}, "a", base, base.Add(time.Hour))
	if facts.Items == nil {
		t.Error("Items = nil, want a non-nil empty slice")
	}
	if len(facts.Items) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(facts.Items))
	}
	if !facts.Truncated {
		t.Error("Truncated = false, want true (passed through from the fetch)")
	}
}

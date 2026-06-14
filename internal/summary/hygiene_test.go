package summary

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

func hygieneParams() HygieneParams {
	return HygieneParams{
		AreaPrefixes:        []reduce.PrefixRule{{Prefix: "area", Delimiter: "/"}},
		DeferredLabels:      []string{"deferred"},
		UnmilestonedAgeDays: 30,
		StaleThresholdDays:  30,
		ContextBodyLength:   1, // a deferred issue needs a non-empty body to have "context"
	}
}

// TestReduceHygieneSignalPredicates pins each of the four signals against a
// crafted issue set, so the exact predicate boundaries are locked.
func TestReduceHygieneSignalPredicates(t *testing.T) {
	deferredEmpty := mkIssue(4, 1, 1, []string{"area/net", "deferred"}, msRef(1, "m"))
	deferredEmpty.BodyText = "   " // whitespace-only → below context length
	deferredWithCtx := mkIssue(5, 1, 1, []string{"area/net", "deferred"}, msRef(1, "m"))
	deferredWithCtx.BodyText = "real explanation of why this is parked"

	issues := []github.Issue{
		mkIssue(1, 1, 1, []string{"bug"}, msRef(1, "m")),       // missing area (no area label)
		mkIssue(2, 40, 1, []string{"area/net"}, nil),           // unmilestoned + aged (age 40 >= 30)
		mkIssue(3, 1, 40, []string{"area/net"}, msRef(1, "m")), // stale (inactive 40 >= 30)
		deferredEmpty,   // deferred-without-context
		deferredWithCtx, // deferred WITH context — not flagged
	}
	facts := ReduceHygiene(issues, 5, hygieneParams(), 20, now)

	if facts.MissingArea.Count != 1 || facts.MissingArea.Issues[0].Number != 1 {
		t.Errorf("missingArea = %+v, want [issue 1]", facts.MissingArea)
	}
	if facts.UnmilestonedAged.Count != 1 || facts.UnmilestonedAged.Issues[0].Number != 2 {
		t.Errorf("unmilestonedAged = %+v, want [issue 2]", facts.UnmilestonedAged)
	}
	if facts.Stale.Count != 1 || facts.Stale.Issues[0].Number != 3 {
		t.Errorf("stale = %+v, want [issue 3]", facts.Stale)
	}
	if facts.DeferredWithoutContext.Count != 1 || facts.DeferredWithoutContext.Issues[0].Number != 4 {
		t.Errorf("deferredWithoutContext = %+v, want [issue 4] (issue 5 has a body)", facts.DeferredWithoutContext)
	}
}

// TestReduceHygieneFreshUnmilestonedNotFlagged pins the age boundary: a young
// unmilestoned issue is normal in-flight work, not a hygiene signal.
func TestReduceHygieneFreshUnmilestonedNotFlagged(t *testing.T) {
	issues := []github.Issue{
		mkIssue(1, 29, 1, []string{"area/net"}, nil), // age 29 < 30 → not aged
	}
	facts := ReduceHygiene(issues, 1, hygieneParams(), 20, now)
	if facts.UnmilestonedAged.Count != 0 {
		t.Errorf("unmilestonedAged = %d, want 0 (age 29 below the 30-day bar)", facts.UnmilestonedAged.Count)
	}
}

// TestReduceHygieneCountUncappedListCapped pins the count-vs-list contract on a
// signal: the count is the full total even when the list is capped.
func TestReduceHygieneCountUncappedListCapped(t *testing.T) {
	issues := []github.Issue{
		mkIssue(1, 1, 40, nil, msRef(1, "m")),
		mkIssue(2, 1, 41, nil, msRef(1, "m")),
		mkIssue(3, 1, 42, nil, msRef(1, "m")),
	}
	// All three are stale and area-missing; cap the list at 1.
	facts := ReduceHygiene(issues, 3, hygieneParams(), 1, now)
	if facts.Stale.Count != 3 {
		t.Errorf("stale count = %d, want 3 (uncapped)", facts.Stale.Count)
	}
	if len(facts.Stale.Issues) != 1 || !facts.Stale.ListTruncated {
		t.Errorf("stale list = %d truncated=%v, want 1/true", len(facts.Stale.Issues), facts.Stale.ListTruncated)
	}
}

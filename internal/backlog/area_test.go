package backlog

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

var areaPrefix = []reduce.PrefixRule{{Prefix: "area", Delimiter: "/"}}

func TestReduceAreaBalanceCountsAndOrder(t *testing.T) {
	issues := []github.Issue{
		labeledIssue(1, 0, "area/networking"),
		labeledIssue(2, 0, "area/networking"),
		labeledIssue(3, 0, "area/storage"),
	}
	facts := ReduceAreaBalance(issues, 3, nil, areaPrefix)
	want := []AreaCount{{Area: "networking", Count: 2}, {Area: "storage", Count: 1}}
	if len(facts.Areas) != 2 || facts.Areas[0] != want[0] || facts.Areas[1] != want[1] {
		t.Errorf("Areas = %+v, want %+v (count desc)", facts.Areas, want)
	}
	if facts.Unclassified != 0 || facts.MultiAreaCount != 0 {
		t.Errorf("Unclassified=%d MultiAreaCount=%d, want 0/0", facts.Unclassified, facts.MultiAreaCount)
	}
}

func TestReduceAreaBalanceMultiAreaOverlap(t *testing.T) {
	// One issue spanning two areas counts in each, so the per-area counts sum
	// above the issue total — expected for overlapping areas.
	issues := []github.Issue{
		labeledIssue(1, 0, "area/a", "area/b"),
		labeledIssue(2, 0, "area/a"),
	}
	facts := ReduceAreaBalance(issues, 2, nil, areaPrefix)
	if facts.MultiAreaCount != 1 {
		t.Errorf("MultiAreaCount = %d, want 1", facts.MultiAreaCount)
	}
	total := 0
	for _, a := range facts.Areas {
		total += a.Count
	}
	if total != 3 {
		t.Errorf("sum of area counts = %d, want 3 (overlap; a=2,b=1)", total)
	}
}

func TestReduceAreaBalanceNormalizedKeyCollapse(t *testing.T) {
	// M1: an explicit-list form ("Core") and a prefix suffix ("core") collapse to
	// one area, keyed by the normalized name, with a deterministic display.
	issues := []github.Issue{labeledIssue(1, 0, "Core", "area/core")}
	facts := ReduceAreaBalance(issues, 1, []string{"Core"}, areaPrefix)
	if len(facts.Areas) != 1 {
		t.Fatalf("Areas = %+v, want one collapsed area", facts.Areas)
	}
	if facts.Areas[0] != (AreaCount{Area: "Core", Count: 1}) {
		t.Errorf("Areas[0] = %+v, want {Core 1}", facts.Areas[0])
	}
	if facts.MultiAreaCount != 0 {
		t.Errorf("MultiAreaCount = %d, want 0 (one conceptual area)", facts.MultiAreaCount)
	}
}

func TestReduceAreaBalanceUnclassified(t *testing.T) {
	issues := []github.Issue{labeledIssue(1, 0, "bug"), labeledIssue(2, 0, "area/x")}
	facts := ReduceAreaBalance(issues, 2, nil, areaPrefix)
	if facts.Unclassified != 1 {
		t.Errorf("Unclassified = %d, want 1", facts.Unclassified)
	}
	if len(facts.Areas) != 1 || facts.Areas[0].Area != "x" {
		t.Errorf("Areas = %+v, want [{x 1}]", facts.Areas)
	}
}

func TestReduceAreaBalanceTieBreakByName(t *testing.T) {
	issues := []github.Issue{labeledIssue(1, 0, "area/zebra"), labeledIssue(2, 0, "area/alpha")}
	facts := ReduceAreaBalance(issues, 2, nil, areaPrefix)
	if facts.Areas[0].Area != "alpha" {
		t.Errorf("tie not broken by name: got %q first, want alpha", facts.Areas[0].Area)
	}
}

func TestReduceAreaBalanceExactOpenAndFetchTruncation(t *testing.T) {
	issues := []github.Issue{labeledIssue(1, 0, "area/x"), labeledIssue(2, 0, "area/x")}
	facts := ReduceAreaBalance(issues, 500, nil, areaPrefix)
	if facts.OpenIssueCount != 500 {
		t.Errorf("OpenIssueCount = %d, want 500 (exact)", facts.OpenIssueCount)
	}
	if facts.FetchedCount != 2 || !facts.FetchTruncated {
		t.Errorf("FetchedCount=%d FetchTruncated=%v, want 2/true", facts.FetchedCount, facts.FetchTruncated)
	}
}

func TestReduceAreaBalanceEmptyMatcher(t *testing.T) {
	issues := []github.Issue{labeledIssue(1, 0, "area/x")}
	facts := ReduceAreaBalance(issues, 1, nil, nil)
	if len(facts.Areas) != 0 {
		t.Errorf("Areas = %+v, want empty (no matcher rules)", facts.Areas)
	}
	if facts.Unclassified != 1 {
		t.Errorf("Unclassified = %d, want 1", facts.Unclassified)
	}
}

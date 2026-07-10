package summary

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

var prefixes = []reduce.PrefixRule{{Prefix: "area", Delimiter: "/"}}

// TestReduceAreaInventorySplitsActiveAndDeferred pins the per-area active/deferred
// split, the multi-area count-once-per-area rule, and the unclassified bucket.
func TestReduceAreaInventorySplitsActiveAndDeferred(t *testing.T) {
	issues := []github.Issue{
		mkIssue(1, 1, 1, []string{"area/net"}, nil),             // net active
		mkIssue(2, 1, 1, []string{"area/net", "deferred"}, nil), // net deferred
		mkIssue(3, 1, 1, []string{"area/net", "area/fs"}, nil),  // net + fs, both active
		mkIssue(4, 1, 1, []string{"bug"}, nil),                  // unclassified active
		mkIssue(5, 1, 1, []string{"deferred"}, nil),             // unclassified deferred
	}
	facts := ReduceAreaInventory(issues, 5, nil, prefixes, []string{"deferred"})

	got := map[string]AreaCount{}
	for _, a := range facts.Areas {
		got[a.Area] = a
	}
	if net := got["net"]; net.Active != 2 || net.Deferred != 1 {
		t.Errorf("net = %+v, want active 2 / deferred 1", net)
	}
	if fs := got["fs"]; fs.Active != 1 || fs.Deferred != 0 {
		t.Errorf("fs = %+v, want active 1 / deferred 0", fs)
	}
	if facts.Unclassified.Active != 1 || facts.Unclassified.Deferred != 1 {
		t.Errorf("unclassified = %+v, want active 1 / deferred 1", facts.Unclassified)
	}
}

// TestReduceAreaInventoryDisplayNameMatchesBacklog pins that the canonical display
// name for an area is the lexicographically-smallest original form across *all*
// matching labels — the same rule backlog.ReduceAreaBalance applies — so the two
// tools never disagree on the same area's name. Here one issue carries an explicit
// "Core" and a prefix "area/core", both normalizing to key "core"; the smaller
// original form is "Core", not the per-issue last-seen "core".
func TestReduceAreaInventoryDisplayNameMatchesBacklog(t *testing.T) {
	issues := []github.Issue{
		mkIssue(1, 1, 1, []string{"Core", "area/core"}, nil),
	}
	facts := ReduceAreaInventory(issues, 1, []string{"Core"}, prefixes, nil)
	if len(facts.Areas) != 1 || facts.Areas[0].Area != "Core" {
		t.Errorf("areas = %+v, want a single area named %q (global-min original form)", facts.Areas, "Core")
	}
}

// TestReduceAreaInventoryOrdersByBusiestAndExactCount pins the busiest-first
// ordering and that OpenIssueCount stays exact under fetch truncation.
func TestReduceAreaInventoryOrdersByBusiestAndExactCount(t *testing.T) {
	issues := []github.Issue{
		mkIssue(1, 1, 1, []string{"area/net"}, nil),
		mkIssue(2, 1, 1, []string{"area/net"}, nil),
		mkIssue(3, 1, 1, []string{"area/fs"}, nil),
	}
	facts := ReduceAreaInventory(issues, 500, nil, prefixes, nil)
	if facts.OpenIssueCount != 500 || !facts.FetchTruncated {
		t.Errorf("OpenIssueCount=%d FetchTruncated=%v, want 500/true", facts.OpenIssueCount, facts.FetchTruncated)
	}
	if len(facts.Areas) != 2 || facts.Areas[0].Area != "net" {
		t.Errorf("areas = %+v, want net first (busiest)", facts.Areas)
	}
}

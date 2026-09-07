package dependency

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// edges builds a slice of open native dependency edges. The fetch layer already
// drops closed and cross-repo edges, so a fetched open edge is what these mirror.
func edges(nums ...int) []github.DependencyRef {
	refs := make([]github.DependencyRef, 0, len(nums))
	for _, n := range nums {
		refs = append(refs, github.DependencyRef{Number: n, Open: true})
	}
	return refs
}

func issue(num int) github.Issue {
	return github.Issue{Number: num, Title: "t", URL: "u"}
}

// bothSeams is the GitHub-shaped forge — every relationship carried — which is what
// the cases below assume unless they are about a forge that carries less.
var bothSeams = github.Capabilities{}

// TestReduceClassifiesBlockedAndGates pins the #87 scenario: a capstone issue
// blocked by several others, with no deferred convention in play. The blocked
// issue surfaces with its authoritative blocked-by edges (correcting the
// mention-graph inversion), and the blockers surface as gates.
func TestReduceClassifiesBlockedAndGates(t *testing.T) {
	capstone := issue(7)
	capstone.BlockedBy = edges(42, 43, 44, 45, 46)
	gate := func(n int) github.Issue {
		is := issue(n)
		is.Blocking = edges(7)
		return is
	}
	issues := []github.Issue{capstone, gate(42), gate(43), gate(44), gate(45), gate(46)}
	facts := Reduce(issues, 6, 20, bothSeams)

	if facts.BlockedCount != 1 {
		t.Errorf("BlockedCount = %d, want 1 (only #7)", facts.BlockedCount)
	}
	if facts.ReadyCount != 5 {
		t.Errorf("ReadyCount = %d, want 5 (the five gates)", facts.ReadyCount)
	}
	if len(facts.Blocked) != 1 || facts.Blocked[0].Number != 7 {
		t.Fatalf("Blocked = %+v, want [#7]", facts.Blocked)
	}
	got := facts.Blocked[0].BlockedBy
	if len(got) != 5 || got[0] != 42 || got[4] != 46 {
		t.Errorf("#7 BlockedBy = %v, want [42 43 44 45 46]", got)
	}
	if len(facts.Gates) != 5 || facts.GateCount != 5 {
		t.Fatalf("Gates listed=%d GateCount=%d, want 5/5", len(facts.Gates), facts.GateCount)
	}
	for _, g := range facts.Gates {
		if len(g.Blocking) != 1 || g.Blocking[0] != 7 {
			t.Errorf("gate #%d Blocking = %v, want [7]", g.Number, g.Blocking)
		}
	}
}

// TestReduceSurfacesPerIssueEdgeTruncation pins that a listed issue carries the
// per-issue edge-truncation flags, so a caller can tell a capped edge list — and a
// derived lower-bound gate ordering / blockingCount — from a complete one, the same
// honesty the deferred and recommendation blocks provide.
func TestReduceSurfacesPerIssueEdgeTruncation(t *testing.T) {
	blocked := issue(1)
	blocked.BlockedBy = edges(2)
	blocked.BlockedByTruncated = true // more blockers than the window read
	gate := issue(2)
	gate.Blocking = edges(1)
	gate.BlockingTruncated = true // more downstream than the window read

	facts := Reduce([]github.Issue{blocked, gate}, 2, 20, bothSeams)
	if len(facts.Blocked) != 1 || !facts.Blocked[0].BlockedByTruncated {
		t.Errorf("blocked issue BlockedByTruncated not surfaced: %+v", facts.Blocked)
	}
	if len(facts.Gates) != 1 || !facts.Gates[0].BlockingTruncated {
		t.Errorf("gate BlockingTruncated not surfaced: %+v", facts.Gates)
	}
	// The summary projection carries the gate's blocking truncation, so a caller reads
	// its blockingCount as a lower bound.
	c := facts.Classification()
	if len(c.Gates) != 1 || !c.Gates[0].BlockingTruncated {
		t.Errorf("classification gate BlockingTruncated not surfaced: %+v", c.Gates)
	}
}

// TestClassificationDropsPerIssueEdges pins the summary-side projection: it carries
// the counts and the gate set (with how many each gate unblocks) but not the raw
// per-issue edge lists — the recommendation block already ships those.
func TestClassificationDropsPerIssueEdges(t *testing.T) {
	capstone := issue(7)
	capstone.BlockedBy = edges(42, 43)
	g := issue(42)
	g.Blocking = edges(7, 8) // ready, unblocks two
	facts := Reduce([]github.Issue{capstone, g, issue(8)}, 3, 20, bothSeams)

	c := facts.Classification()
	if c.ReadyCount != facts.ReadyCount || c.BlockedCount != facts.BlockedCount || c.GateCount != facts.GateCount {
		t.Errorf("counts not carried: %+v vs ready=%d blocked=%d gate=%d",
			c, facts.ReadyCount, facts.BlockedCount, facts.GateCount)
	}
	if len(c.Gates) != 1 || c.Gates[0].Number != 42 {
		t.Fatalf("Gates = %+v, want [#42]", c.Gates)
	}
	if c.Gates[0].BlockingCount != 2 {
		t.Errorf("BlockingCount = %d, want 2 (unblocks #7 and #8)", c.Gates[0].BlockingCount)
	}
	if c.Gates == nil {
		t.Error("Gates nil; want non-nil empty slice")
	}
}

// TestReduceReadyIssueWithNoEdgesIsNotAGate: a ready issue that gates nothing is
// counted ready but appears in neither list.
func TestReduceReadyIssueWithNoEdgesIsNotAGate(t *testing.T) {
	facts := Reduce([]github.Issue{issue(1)}, 1, 20, bothSeams)
	if facts.ReadyCount != 1 {
		t.Errorf("ReadyCount = %d, want 1", facts.ReadyCount)
	}
	if len(facts.Gates) != 0 {
		t.Errorf("Gates = %d, want 0 (gates nothing)", len(facts.Gates))
	}
	if len(facts.Blocked) != 0 {
		t.Errorf("Blocked = %d, want 0", len(facts.Blocked))
	}
}

// TestReduceOpenSubIssueGateIsBlocked: a parent with open children is gated even
// with no blocked-by edge and an empty windowed sub-issue list — the authoritative
// total/completed gap witnesses the hidden gate.
func TestReduceOpenSubIssueGateIsBlocked(t *testing.T) {
	parent := issue(1)
	parent.SubIssuesTotal = 3
	parent.SubIssuesCompleted = 1 // two open children, none listed in the window
	facts := Reduce([]github.Issue{parent}, 1, 20, bothSeams)
	if facts.BlockedCount != 1 {
		t.Errorf("BlockedCount = %d, want 1 (open sub-issue gate)", facts.BlockedCount)
	}
	if len(facts.Blocked) != 1 || !facts.Blocked[0].SubIssueGate {
		t.Fatalf("Blocked = %+v, want [#1 with SubIssueGate]", facts.Blocked)
	}
	if facts.ReadyCount != 0 {
		t.Errorf("ReadyCount = %d, want 0 (parent gated by children)", facts.ReadyCount)
	}
}

// TestReduceTruncatedBlockedByIsProvisionalNotReady: an issue with no listed open
// blocked-by but a truncated edge list cannot be confirmed ready, so it is
// provisional — never a false-ready gate.
func TestReduceTruncatedBlockedByIsProvisionalNotReady(t *testing.T) {
	is := issue(1)
	is.BlockedByTruncated = true
	is.Blocking = edges(2)
	facts := Reduce([]github.Issue{is, issue(2)}, 2, 20, bothSeams)
	if facts.ProvisionalCount != 1 {
		t.Errorf("ProvisionalCount = %d, want 1", facts.ProvisionalCount)
	}
	if facts.ReadyCount != 1 {
		t.Errorf("ReadyCount = %d, want 1 (#2 only)", facts.ReadyCount)
	}
	for _, g := range facts.Gates {
		if g.Number == 1 {
			t.Error("provisional issue #1 must not be a confirmed gate")
		}
	}
}

// TestReduceSeamStateClassification walks the seam states an issue presenting no
// blocker can arrive in. The pair that matters is unavailable versus notApplicable:
// both leave every edge list empty, and only the state says whether that emptiness is
// evidence. The unrecognized case pins the fail-safe — a state added later must land
// in provisional rather than fall through to ready.
func TestReduceSeamStateClassification(t *testing.T) {
	cases := []struct {
		name            string
		blockedBy       github.SeamState
		subIssueGap     github.SeamState
		wantReady       int
		wantProvisional int
	}{
		{"unset zero value is available", "", "", 1, 0},
		{"both read", github.SeamAvailable, github.SeamAvailable, 1, 0},
		{"blocked-by unread", github.SeamUnavailable, github.SeamAvailable, 0, 1},
		{"sub-issue gap unread", github.SeamAvailable, github.SeamUnavailable, 0, 1},
		{"both unread", github.SeamUnavailable, github.SeamUnavailable, 0, 1},
		{"no sub-issue carrier", github.SeamAvailable, github.SeamNotApplicable, 1, 0},
		{"no carrier either side", github.SeamNotApplicable, github.SeamNotApplicable, 1, 0},
		{"unrecognized state fails safe", github.SeamState("someLaterState"), github.SeamAvailable, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := issue(1)
			is.BlockedByState = tc.blockedBy
			is.SubIssueGapState = tc.subIssueGap
			facts := Reduce([]github.Issue{is}, 1, 20, bothSeams)
			if facts.ReadyCount != tc.wantReady {
				t.Errorf("ReadyCount = %d, want %d", facts.ReadyCount, tc.wantReady)
			}
			if facts.ProvisionalCount != tc.wantProvisional {
				t.Errorf("ProvisionalCount = %d, want %d", facts.ProvisionalCount, tc.wantProvisional)
			}
		})
	}
}

// TestReduceBlockSeamReportsUnavailableWhenNothingRead: the block-level report has to
// be able to say a carried seam went unread, or its most consequential case is the one
// it cannot express. Where every fetched issue withheld the same seam — the shape a
// repository-wide disabled relationship produces — the block says so once, rather than
// leaving a caller to infer it from a provisional count that equals the fetched count.
func TestReduceBlockSeamReportsUnavailableWhenNothingRead(t *testing.T) {
	var issues []github.Issue
	for n := 1; n <= 3; n++ {
		is := issue(n)
		is.BlockedByState = github.SeamUnavailable
		issues = append(issues, is)
	}

	facts := Reduce(issues, 3, 20, bothSeams)
	if facts.Seams.BlockedBy != github.SeamUnavailable {
		t.Errorf("Seams.BlockedBy = %v, want unavailable (no fetched issue yielded the seam)", facts.Seams.BlockedBy)
	}
	if facts.Seams.SubIssueGap != github.SeamAvailable {
		t.Errorf("Seams.SubIssueGap = %v, want available (that seam read fine)", facts.Seams.SubIssueGap)
	}
	if facts.ProvisionalCount != 3 {
		t.Errorf("ProvisionalCount = %d, want 3", facts.ProvisionalCount)
	}
}

// TestReduceBlockSeamStaysAvailableWhenPartiallyRead: one issue's failed read is not
// the forge's verdict. A mixed window keeps the block-level state available — the
// per-issue states and the provisional count carry the individual failures — so a
// single unreadable issue cannot misreport the whole repository.
func TestReduceBlockSeamStaysAvailableWhenPartiallyRead(t *testing.T) {
	unread := issue(1)
	unread.BlockedByState = github.SeamUnavailable

	facts := Reduce([]github.Issue{unread, issue(2)}, 2, 20, bothSeams)
	if facts.Seams.BlockedBy != github.SeamAvailable {
		t.Errorf("Seams.BlockedBy = %v, want available (one issue's failure is not the forge's)", facts.Seams.BlockedBy)
	}
	if facts.ProvisionalCount != 1 {
		t.Errorf("ProvisionalCount = %d, want 1", facts.ProvisionalCount)
	}
}

// TestReduceBlockSeamEmptyWindowIsNotUnavailable: with nothing fetched there is no
// evidence either way, and "every issue withheld it" is vacuously true over an empty
// set. Reporting unavailable there would manufacture a fault from an empty backlog.
func TestReduceBlockSeamEmptyWindowIsNotUnavailable(t *testing.T) {
	facts := Reduce(nil, 0, 20, bothSeams)
	if facts.Seams.BlockedBy != github.SeamAvailable {
		t.Errorf("Seams.BlockedBy = %v, want available (an empty window is not evidence of a fault)", facts.Seams.BlockedBy)
	}
}

// TestReduceUncarriedSeamOverridesPerIssueState: where the forge carries no sub-issue
// hierarchy, no read of it can have failed, so a per-issue unavailable is a
// contradiction rather than a second opinion and the forge-level fact wins. Without
// this the first backend to set both would report every issue unconfirmable.
func TestReduceUncarriedSeamOverridesPerIssueState(t *testing.T) {
	is := issue(1)
	is.SubIssueGapState = github.SeamUnavailable
	caps := github.Capabilities{NoSubIssueHierarchy: true}

	facts := Reduce([]github.Issue{is}, 1, 20, caps)
	if facts.ReadyCount != 1 {
		t.Errorf("ReadyCount = %d, want 1 (an uncarried seam withholds nothing)", facts.ReadyCount)
	}
	if facts.Seams.SubIssueGap != github.SeamNotApplicable {
		t.Errorf("Seams.SubIssueGap = %v, want notApplicable", facts.Seams.SubIssueGap)
	}
}

// TestReduceOpenSubIssueGateIgnoredWhereUncarried: the open-child gap is only
// evidence where the forge has children at all. A backend leaving the counts zero
// because the concept does not exist must not have that read as a cleared gate — nor,
// as the sibling case above pins, as an unconfirmable one.
func TestReduceOpenSubIssueGateIgnoredWhereUncarried(t *testing.T) {
	parent := issue(1)
	parent.SubIssuesTotal = 3
	parent.SubIssuesCompleted = 1
	caps := github.Capabilities{NoSubIssueHierarchy: true}

	facts := Reduce([]github.Issue{parent}, 1, 20, caps)
	if facts.BlockedCount != 0 || facts.ReadyCount != 1 {
		t.Errorf("blocked=%d ready=%d, want 0/1 (counts from an uncarried seam are not a gate)",
			facts.BlockedCount, facts.ReadyCount)
	}
}

// TestReduceFetchTruncationAndNonNilSlices: OpenIssueCount stays exact under a
// truncated window, and the lists are non-nil so they serialize as [].
func TestReduceFetchTruncationAndNonNilSlices(t *testing.T) {
	facts := Reduce([]github.Issue{issue(1)}, 500, 20, bothSeams)
	if facts.OpenIssueCount != 500 || !facts.FetchTruncated {
		t.Errorf("OpenIssueCount=%d FetchTruncated=%v, want 500/true", facts.OpenIssueCount, facts.FetchTruncated)
	}
	if facts.Gates == nil || facts.Blocked == nil {
		t.Error("Gates/Blocked are nil; want non-nil empty slices")
	}
}

// TestReduceListTruncation: the gate list caps at the limit while the count does
// not, and the truncation flag is set.
func TestReduceListTruncation(t *testing.T) {
	issues := []github.Issue{}
	for n := 1; n <= 3; n++ {
		g := issue(n)
		g.Blocking = edges(100)
		issues = append(issues, g)
	}
	downstream := issue(100)
	downstream.BlockedBy = edges(1, 2, 3)
	issues = append(issues, downstream)

	facts := Reduce(issues, 4, 2, bothSeams)
	if len(facts.Gates) != 2 || !facts.GatesTruncated {
		t.Errorf("Gates listed=%d truncated=%v, want 2/true", len(facts.Gates), facts.GatesTruncated)
	}
	if facts.ReadyCount != 3 {
		t.Errorf("ReadyCount = %d, want 3 (count not capped)", facts.ReadyCount)
	}
}

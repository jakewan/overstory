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

// TestReduceRecommendationsBodyRefs pins the dependency-readiness signal: each
// candidate carries the distinct #N references parsed from its body (the rendered
// plaintext bodyText), PR references and the issue's own number excluded — so a
// caller's "what to start next" ranking can tell a ready issue from one gated
// behind open siblings. Self-exclusion is checked both alongside real refs and
// alone (where it empties the slice, which must stay non-nil to serialize as []).
func TestReduceRecommendationsBodyRefs(t *testing.T) {
	withRefs := mkIssue(1, 10, 2, nil, nil)
	// Plaintext body: a duplicate ref, a PR reference, and a self-reference.
	withRefs.BodyText = "Blocked by #10 and #11. Also tracks #10. Self ref #1. Needs PR #99 first."
	selfOnly := mkIssue(7, 5, 1, nil, nil)
	selfOnly.BodyText = "Depends on #7 only."

	facts := ReduceRecommendations([]github.Issue{withRefs, selfOnly}, 2, nil, 20, now)

	// The neutral pre-sort (bugs/oldest/number) reorders candidates, so select by
	// issue number rather than slice index.
	byNumber := make(map[int]RecommendationCandidate, len(facts.Candidates))
	for _, c := range facts.Candidates {
		byNumber[c.Number] = c
	}

	got1, ok := byNumber[1]
	if !ok {
		t.Fatal("candidate #1 missing")
	}
	wantRefs := []int{10, 11}
	if len(got1.BodyRefs) != len(wantRefs) || got1.BodyRefs[0] != 10 || got1.BodyRefs[1] != 11 {
		t.Errorf("candidate 1 BodyRefs = %v, want %v (deduped, sorted, PR + self excluded)", got1.BodyRefs, wantRefs)
	}
	got7, ok := byNumber[7]
	if !ok {
		t.Fatal("candidate #7 missing")
	}
	if got7.BodyRefs == nil {
		t.Error("candidate 7 BodyRefs = nil, want non-nil empty slice (self-only, serializes as [])")
	}
	if len(got7.BodyRefs) != 0 {
		t.Errorf("candidate 7 BodyRefs = %v, want empty (only a self-reference)", got7.BodyRefs)
	}
}

// blk builds open native dependency edges to the given issue numbers, for the
// blocking/blocked-by direction a test needs to exercise.
func blk(nums ...int) []github.DependencyRef {
	edges := make([]github.DependencyRef, len(nums))
	for i, n := range nums {
		edges[i] = github.DependencyRef{Number: n, Open: true}
	}
	return edges
}

// byNum indexes candidates by issue number so a test reads a specific candidate
// regardless of the neutral pre-sort's ordering.
func byNum(cands []RecommendationCandidate) map[int]RecommendationCandidate {
	m := make(map[int]RecommendationCandidate, len(cands))
	for _, c := range cands {
		m[c.Number] = c
	}
	return m
}

// TestReduceRecommendationsGatesPrioritized pins the join (#97): a candidate's
// gatesPrioritized is the subset of its open blocking edges whose target is
// milestoned or bug-labeled within the fetched window — the "which prioritized
// work this candidate unblocks" signal a caller cannot derive itself (the target
// may be past the list cap, and openIssueSet carries no milestone/label). The
// signal is emitted regardless of the candidate's own readiness.
func TestReduceRecommendationsGatesPrioritized(t *testing.T) {
	// Fetched targets: 50 milestoned, 51 bug-labeled, 52 plain (not prioritized).
	p := mkIssue(50, 20, 1, nil, msRef(7, "M"))
	b := mkIssue(51, 20, 1, []string{"bug"}, nil)
	n := mkIssue(52, 20, 1, nil, nil)
	// A ready candidate gating all three plus a closed issue.
	ready := mkIssue(10, 30, 1, nil, nil)
	ready.Blocking = []github.DependencyRef{{Number: 50, Open: true}, {Number: 51, Open: true}, {Number: 52, Open: true}, {Number: 53, Open: false}}
	// A blocked candidate still reports what it gates.
	blocked := mkIssue(20, 30, 1, nil, nil)
	blocked.Blocking = blk(50)
	blocked.BlockedBy = blk(99)

	facts := ReduceRecommendations([]github.Issue{p, b, n, ready, blocked}, 5, []string{"bug"}, 20, now)
	got := byNum(facts.Candidates)

	if g := got[10].GatesPrioritized; len(g) != 2 || g[0] != 50 || g[1] != 51 {
		t.Errorf("candidate 10 GatesPrioritized = %v, want [50 51] (milestoned + bug; plain #52 and closed #53 excluded)", g)
	}
	if g := got[20].GatesPrioritized; len(g) != 1 || g[0] != 50 {
		t.Errorf("candidate 20 GatesPrioritized = %v, want [50] (emitted despite being blocked)", g)
	}
	// A candidate gating only non-prioritized work reports an empty, non-nil slice.
	if g := got[52].GatesPrioritized; g == nil {
		t.Error("candidate 52 GatesPrioritized = nil, want non-nil empty slice (serializes as [])")
	} else if len(g) != 0 {
		t.Errorf("candidate 52 GatesPrioritized = %v, want empty", g)
	}
}

// TestReduceRecommendationsReservesNewestHighLeverageGate pins Finding 1: when the
// eligible gate band exceeds the cap, the reserve keeps the highest-leverage /
// newest ready gate, not the oldest — so a freshly-filed blocker of prioritized
// work survives the cap rather than being evicted by an oldest-first sort.
func TestReduceRecommendationsReservesNewestHighLeverageGate(t *testing.T) {
	// Three milestoned targets so leverage can differ.
	p := mkIssue(50, 20, 1, nil, msRef(7, "M"))
	q := mkIssue(51, 20, 1, nil, msRef(7, "M"))
	s := mkIssue(52, 20, 1, nil, msRef(7, "M"))
	// Newest gate, highest leverage (gates all three).
	newHigh := mkIssue(12, 1, 1, nil, nil)
	newHigh.Blocking = blk(50, 51, 52)
	// Older gates, single leverage.
	oldMid := mkIssue(11, 50, 1, nil, nil)
	oldMid.Blocking = blk(50)
	oldOld := mkIssue(10, 100, 1, nil, nil)
	oldOld.Blocking = blk(50)

	// limit 2 → reserve = min(5, 2/2) = 1 slot, which must go to the newest/highest.
	facts := ReduceRecommendations([]github.Issue{p, q, s, newHigh, oldMid, oldOld}, 6, []string{"bug"}, 2, now)
	got := byNum(facts.Candidates)
	if _, ok := got[12]; !ok {
		t.Error("candidate 12 (newest, highest-leverage gate) absent — evicted by oldest-first (Finding 1)")
	}
	if _, ok := got[11]; ok {
		t.Error("candidate 11 (older, lower-leverage gate) present — should lose the single reserve slot to #12")
	}
}

// TestReduceRecommendationsReserveTieBreakPrefersNewest pins that when two ready
// gates tie on both leverage and age, the reserve seats the newer (higher-numbered)
// one — a same-day-filed fresh blocker should win the scarce slot, not lose it to an
// older sibling.
func TestReduceRecommendationsReserveTieBreakPrefersNewest(t *testing.T) {
	p := mkIssue(50, 20, 1, nil, msRef(7, "M"))
	// Two gates, same leverage (both gate P) and same age.
	older := mkIssue(11, 1, 1, nil, nil)
	older.Blocking = blk(50)
	newer := mkIssue(12, 1, 1, nil, nil)
	newer.Blocking = blk(50)
	// Oldest filler takes the non-reserve slot, so absence proves the reserve, not the fill.
	filler := mkIssue(60, 100, 1, nil, nil)

	// limit 2 → reserve 1, which must seat the newer (higher-numbered) gate on the tie.
	facts := ReduceRecommendations([]github.Issue{p, older, newer, filler}, 4, []string{"bug"}, 2, now)
	got := byNum(facts.Candidates)
	if _, ok := got[12]; !ok {
		t.Error("candidate 12 (newer, tied gate) absent — the reserve seated the older sibling")
	}
	if _, ok := got[11]; ok {
		t.Error("candidate 11 (older, tied gate) present — it should lose the tie to the newer #12")
	}
}

// TestReduceRecommendationsReserveDoesNotStarveBugs pins Finding 2: an over-cap
// gate band cannot push every bug out of the candidate list — the reserve is
// bounded so the normal bugs-first pre-sort keeps at least half the slots.
func TestReduceRecommendationsReserveDoesNotStarveBugs(t *testing.T) {
	p := mkIssue(50, 20, 1, nil, msRef(7, "M"))
	bug := mkIssue(5, 5, 1, []string{"bug"}, nil)
	gates := []github.Issue{}
	for _, num := range []int{10, 11, 12, 13} {
		g := mkIssue(num, num, 1, nil, nil) // ages 10..13 so all are ready gates
		g.Blocking = blk(50)
		gates = append(gates, g)
	}
	issues := append([]github.Issue{p, bug}, gates...)

	// limit 3 → reserve = min(5, 3/2) = 1, leaving 2 slots for the bugs-first fill.
	facts := ReduceRecommendations(issues, len(issues), []string{"bug"}, 3, now)
	got := byNum(facts.Candidates)
	if _, ok := got[5]; !ok {
		t.Error("bug #5 absent — an over-cap gate band starved the bug band (Finding 2)")
	}
}

// TestReduceRecommendationsBlockedGateNotReserved pins that readiness is
// load-bearing: an itself-blocked gate of prioritized work is not promoted into
// the reserve (it is not actionable now and would surface transitively through its
// own gate root).
func TestReduceRecommendationsBlockedGateNotReserved(t *testing.T) {
	p := mkIssue(50, 20, 1, nil, msRef(7, "M"))
	// Ready gate, older.
	readyGate := mkIssue(12, 100, 1, nil, nil)
	readyGate.Blocking = blk(50)
	// Blocked gate, newest — would win the reserve on leverage/recency if readiness
	// were ignored.
	blockedGate := mkIssue(11, 1, 1, nil, nil)
	blockedGate.Blocking = blk(50)
	blockedGate.BlockedBy = blk(99)
	// Oldest filler to take the non-reserve slot.
	filler := mkIssue(60, 200, 1, nil, nil)

	// limit 2 → reserve 1 goes to the ready gate; fill takes the oldest (filler).
	facts := ReduceRecommendations([]github.Issue{p, readyGate, blockedGate, filler}, 4, []string{"bug"}, 2, now)
	got := byNum(facts.Candidates)
	if _, ok := got[12]; !ok {
		t.Error("ready gate #12 absent — readiness-eligible gate lost the reserve")
	}
	if _, ok := got[11]; ok {
		t.Error("blocked gate #11 present — a non-ready gate must not take the reserve slot")
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

package criticalpath

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// areaPrefix is the area taxonomy shared by these cases: an `area/<name>` label
// classifies an issue into stream `<name>`.
var areaPrefix = []reduce.PrefixRule{{Prefix: "area", Delimiter: "/"}}

func issue(num int, labels ...string) github.Issue {
	return github.Issue{Number: num, Title: "issue", Labels: labels}
}

func params(streams ...string) Params {
	return Params{Streams: streams, Label: "critical-path", AreaPrefixes: areaPrefix}
}

// streamByName finds an emitted stream by its display name.
func streamByName(facts Facts, name string) (Stream, bool) {
	for _, s := range facts.Streams {
		if s.Stream == name {
			return s, true
		}
	}
	return Stream{}, false
}

// TestReduceNotConfigured: with no declared streams the reduction is a no-op — not
// an error — carrying no streams. It is still a successful reduction (Available),
// distinct from a degraded fetch, which the handler marks Available:false.
func TestReduceNotConfigured(t *testing.T) {
	facts := Reduce([]github.Issue{issue(1, "critical-path", "area/simulation")}, true, Params{}, 20)
	if facts.Configured {
		t.Errorf("Configured = true, want false")
	}
	if !facts.Available {
		t.Errorf("Available = false, want true (a no-op is a successful reduction, not a degraded fetch)")
	}
	if len(facts.Streams) != 0 {
		t.Errorf("Streams = %+v, want empty", facts.Streams)
	}
}

// TestReduceStreamsWithoutLabelIsNotConfigured: streams declared without a label
// (or with a whitespace-only one) cannot classify any issue, so the reduction is a
// no-op rather than reporting configured and clearing every gate. The manifest
// layer rejects this, but the reduction guards it too so a direct caller can't
// produce a lying gate.
func TestReduceStreamsWithoutLabelIsNotConfigured(t *testing.T) {
	for _, label := range []string{"", "   "} {
		facts := Reduce(
			[]github.Issue{issue(1, "critical-path", "area/simulation")},
			true,
			Params{Streams: []string{"simulation"}, Label: label, AreaPrefixes: areaPrefix},
			20,
		)
		if facts.Configured {
			t.Errorf("label %q: Configured = true, want false (no label ⇒ cannot classify)", label)
		}
		if len(facts.Streams) != 0 {
			t.Errorf("label %q: Streams = %+v, want empty no-op", label, facts.Streams)
		}
	}
}

// TestReducePreservesDeclaredOrder: streams are emitted in declared order, not
// sorted by member count — the property that distinguishes this from areaInventory.
func TestReducePreservesDeclaredOrder(t *testing.T) {
	issues := []github.Issue{
		issue(1, "critical-path", "area/ui"), // only ui has a member
		issue(2, "critical-path", "area/ui"),
	}
	facts := Reduce(issues, true, params("simulation", "narrative", "ui"), 20)
	want := []string{"simulation", "narrative", "ui"}
	for i, w := range want {
		if facts.Streams[i].Stream != w {
			t.Errorf("Streams[%d] = %q, want %q (declared order)", i, facts.Streams[i].Stream, w)
		}
	}
}

// TestReduceGateClearedVsUncleared: a stream with an open critical-path member is
// uncleared; one with none is cleared. A complete source set reports the reduction
// available and the fetch not truncated, so the gate is authoritative.
func TestReduceGateClearedVsUncleared(t *testing.T) {
	issues := []github.Issue{issue(1, "critical-path", "area/simulation")}
	facts := Reduce(issues, true, params("simulation", "narrative"), 20)

	if !facts.Available {
		t.Errorf("Available = false, want true")
	}
	if facts.FetchTruncated {
		t.Errorf("FetchTruncated = true, want false (complete source set)")
	}
	sim, _ := streamByName(facts, "simulation")
	if sim.GateCleared {
		t.Errorf("simulation GateCleared = true, want false (open member)")
	}
	if len(sim.Members) != 1 || sim.Members[0].Number != 1 {
		t.Errorf("simulation Members = %+v, want [#1]", sim.Members)
	}
	nar, _ := streamByName(facts, "narrative")
	if !nar.GateCleared || len(nar.Members) != 0 {
		t.Errorf("narrative = %+v, want cleared with no members", nar)
	}
}

// TestReduceGateBeforeCap: GateCleared is computed from the full matched set before
// the list cap, so even listLimit:0 (which empties the member list) reports an
// uncleared gate for a populated stream.
func TestReduceGateBeforeCap(t *testing.T) {
	issues := []github.Issue{
		issue(1, "critical-path", "area/simulation"),
		issue(2, "critical-path", "area/simulation"),
	}
	facts := Reduce(issues, true, params("simulation"), 0)
	sim, _ := streamByName(facts, "simulation")
	if sim.GateCleared {
		t.Errorf("GateCleared = true under listLimit:0, want false (gate reads pre-cap count)")
	}
	if !sim.ListTruncated || len(sim.Members) != 0 {
		t.Errorf("Members = %+v ListTruncated = %v, want empty+truncated under listLimit:0", sim.Members, sim.ListTruncated)
	}
}

// TestReduceMemberOrderAndCap: members are ascending by issue number, and which
// members survive the cap is deterministic (the lowest numbers).
func TestReduceMemberOrderAndCap(t *testing.T) {
	// Supplied out of order to prove the reduction sorts rather than echoing input.
	issues := []github.Issue{
		issue(5, "critical-path", "area/simulation"),
		issue(2, "critical-path", "area/simulation"),
		issue(8, "critical-path", "area/simulation"),
	}
	facts := Reduce(issues, true, params("simulation"), 2)
	sim, _ := streamByName(facts, "simulation")
	if !sim.ListTruncated {
		t.Fatalf("ListTruncated = false, want true (3 members, limit 2)")
	}
	if len(sim.Members) != 2 || sim.Members[0].Number != 2 || sim.Members[1].Number != 5 {
		t.Errorf("Members = %+v, want [#2 #5] (ascending, lowest survive cap)", sim.Members)
	}
}

// TestReduceMultiStreamMember: an issue labeled for two declared streams is a
// member of each (overlapping, like areaInventory) and blocks both gates.
func TestReduceMultiStreamMember(t *testing.T) {
	issues := []github.Issue{issue(1, "critical-path", "area/simulation", "area/narrative")}
	facts := Reduce(issues, true, params("simulation", "narrative"), 20)
	for _, name := range []string{"simulation", "narrative"} {
		s, _ := streamByName(facts, name)
		if s.GateCleared || len(s.Members) != 1 {
			t.Errorf("%s = %+v, want uncleared with member #1", name, s)
		}
	}
	if facts.OffPathCount != 0 {
		t.Errorf("OffPathCount = %d, want 0 (on-path issue not double-counted)", facts.OffPathCount)
	}
	// One critical-path issue in hand — FetchedCount counts the matched subset, not
	// the stream-membership fan-out (the issue is a member of two streams but one fetch).
	if facts.FetchedCount != 1 {
		t.Errorf("FetchedCount = %d, want 1 (one critical-path issue)", facts.FetchedCount)
	}
}

// TestReduceOffPathVsUnareaed: a critical-path issue in a real-but-undeclared area
// is off-path; one with no area at all is unareaed. Precedence: an issue that is
// also a member of a declared stream is neither. FetchedCount counts every
// critical-path issue reduced over, regardless of disposition.
func TestReduceOffPathVsUnareaed(t *testing.T) {
	issues := []github.Issue{
		issue(1, "critical-path", "area/tooling"),                    // off-path: real area, not declared
		issue(2, "critical-path"),                                    // unareaed: no area label
		issue(3, "critical-path", "area/simulation", "area/tooling"), // on-path: member wins over off-path
		issue(4, "area/simulation"),                                  // not critical-path → not counted
	}
	facts := Reduce(issues, true, params("simulation", "narrative"), 20)
	if facts.OffPathCount != 1 {
		t.Errorf("OffPathCount = %d, want 1 (issue 1)", facts.OffPathCount)
	}
	if facts.UnareaedCount != 1 {
		t.Errorf("UnareaedCount = %d, want 1 (issue 2)", facts.UnareaedCount)
	}
	if facts.FetchedCount != 3 {
		t.Errorf("FetchedCount = %d, want 3 (issues 1,2,3 are critical-path; issue 4 is not)", facts.FetchedCount)
	}
	sim, _ := streamByName(facts, "simulation")
	if len(sim.Members) != 1 || sim.Members[0].Number != 3 {
		t.Errorf("simulation Members = %+v, want [#3] (precedence: member, not off-path)", sim.Members)
	}
}

// TestReduceIncompleteSourceSet: when the source set is incomplete (the labeled
// fetch itself truncated), FetchTruncated is set so a caller treats an empty
// stream's cleared gate as provisional — an unfetched critical-path issue may
// remain. This is the rare tail: it fires only when the general window truncated
// AND more than the fetch cap of labeled issues exist.
func TestReduceIncompleteSourceSet(t *testing.T) {
	facts := Reduce([]github.Issue{issue(1, "critical-path", "area/simulation")}, false, params("simulation", "narrative"), 20)
	if !facts.FetchTruncated {
		t.Errorf("FetchTruncated = false, want true (incomplete source set)")
	}
	nar, _ := streamByName(facts, "narrative")
	if !nar.GateCleared {
		t.Errorf("narrative GateCleared = false, want true (no member in the incomplete set — provisional)")
	}
}

// TestReduceLabelMatchingIsCaseInsensitive: the critical-path label and stream
// names match an issue's labels case-insensitively (GitHub labels match that way).
func TestReduceLabelMatchingIsCaseInsensitive(t *testing.T) {
	issues := []github.Issue{issue(1, "Critical-Path", "Area/Simulation")}
	facts := Reduce(issues, true, params("simulation"), 20)
	sim, ok := streamByName(facts, "simulation")
	if !ok || sim.GateCleared || len(sim.Members) != 1 {
		t.Errorf("simulation = %+v ok=%v, want member #1 despite label casing", sim, ok)
	}
}

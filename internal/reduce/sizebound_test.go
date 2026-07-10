package reduce

import (
	"encoding/json"
	"strings"
	"testing"
)

// boundFake is a minimal stand-in for a composite Facts: two trimmable detail
// lists plus the marker field, enough to exercise ApplyByteBudget without pulling in
// a real reduction.
type boundFake struct {
	Big        []string        `json:"big"`
	BigTrunc   bool            `json:"bigTruncated"`
	Small      []string        `json:"small"`
	SmallTrunc bool            `json:"smallTruncated"`
	Marker     *SizeBoundFacts `json:"sizeBound,omitempty"`
}

func sliceUnit(block string, list *[]string, trunc *bool) Trimmable {
	origLen := len(*list)
	origTrunc := *trunc
	return Trimmable{
		Block:     block,
		Size:      func() int { return JSONLen(*list) },
		Remaining: func() int { return len(*list) },
		Drop: func() {
			if len(*list) > 0 {
				*list = (*list)[:len(*list)-1]
				*trunc = true
			}
		},
		Restore: func() {
			*list = (*list)[:origLen]
			*trunc = origTrunc
		},
	}
}

func (f *boundFake) bound(maxBytes int) (*SizeBoundFacts, error) {
	return ApplyByteBudget(
		func() ([]byte, error) { return json.Marshal(*f) },
		func(m *SizeBoundFacts) { f.Marker = m },
		maxBytes,
		[]Trimmable{
			sliceUnit("big", &f.Big, &f.BigTrunc),
			sliceUnit("small", &f.Small, &f.SmallTrunc),
		},
	)
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// TestApplyByteBudgetUnderBudgetIsUntouched: a response that already fits gets no
// marker and no trimming — the normal path stays byte-identical.
func TestApplyByteBudgetUnderBudgetIsUntouched(t *testing.T) {
	f := boundFake{Big: repeat("x", 3), Small: repeat("y", 3)}
	m, err := f.bound(100_000)
	if err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	if m != nil || f.Marker != nil {
		t.Errorf("marker = %+v / %+v, want nil on under-budget", m, f.Marker)
	}
	if len(f.Big) != 3 || len(f.Small) != 3 || f.BigTrunc || f.SmallTrunc {
		t.Errorf("lists were trimmed: big=%d small=%d", len(f.Big), len(f.Small))
	}
}

// TestApplyByteBudgetBoundsAndReportsUpperBound: over budget, the actual marshaled
// size is within budget and the self-reported FinalBytes is an upper bound on it
// (the M4 self-reference guard).
func TestApplyByteBudgetBoundsAndReportsUpperBound(t *testing.T) {
	const maxBytes = 600
	f := boundFake{Big: repeat(strings.Repeat("A", 40), 30), Small: repeat("s", 30)}
	m, err := f.bound(maxBytes)
	if err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	if m == nil {
		t.Fatalf("marker = nil, want a marker on over-budget")
	}
	actual := len(mustMarshal(t, f))
	if actual > maxBytes {
		t.Errorf("actual size = %d, want <= %d", actual, maxBytes)
	}
	if m.FinalBytes < actual || m.FinalBytes > maxBytes {
		t.Errorf("FinalBytes = %d, want an upper bound in [%d, %d]", m.FinalBytes, actual, maxBytes)
	}
}

// TestApplyByteBudgetBalancesAcrossBlocks: equal-weight lists are trimmed evenly
// rather than one being emptied first (the m3 fix).
func TestApplyByteBudgetBalancesAcrossBlocks(t *testing.T) {
	f := boundFake{Big: repeat("equal", 20), Small: repeat("equal", 20)}
	if _, err := f.bound(300); err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	if diff := len(f.Big) - len(f.Small); diff < -1 || diff > 1 {
		t.Errorf("unbalanced trim: big=%d small=%d (want within 1)", len(f.Big), len(f.Small))
	}
}

// TestApplyByteBudgetTrimsHeavierBlockMore: with one byte-heavy list and one light
// list, byte-based selection cuts the heavy one more.
func TestApplyByteBudgetTrimsHeavierBlockMore(t *testing.T) {
	f := boundFake{Big: repeat(strings.Repeat("A", 50), 20), Small: repeat("s", 20)}
	if _, err := f.bound(700); err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	if len(f.Big) >= len(f.Small) {
		t.Errorf("heavy block not trimmed more: big=%d small=%d", len(f.Big), len(f.Small))
	}
}

// TestApplyByteBudgetMarkerAccounting: TrimmedBlocks reports dropped + remaining per
// block, summing to the original count.
func TestApplyByteBudgetMarkerAccounting(t *testing.T) {
	f := boundFake{Big: repeat(strings.Repeat("A", 40), 25), Small: repeat("s", 25)}
	m, err := f.bound(500)
	if err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	for _, tb := range m.TrimmedBlocks {
		var orig, remaining int
		switch tb.Block {
		case "big":
			orig, remaining = 25, len(f.Big)
		case "small":
			orig, remaining = 25, len(f.Small)
		default:
			t.Fatalf("unexpected trimmed block %q", tb.Block)
		}
		if tb.Remaining != remaining {
			t.Errorf("%s remaining = %d, want %d", tb.Block, tb.Remaining, remaining)
		}
		if tb.Dropped+tb.Remaining != orig {
			t.Errorf("%s dropped(%d)+remaining(%d) != orig(%d)", tb.Block, tb.Dropped, tb.Remaining, orig)
		}
	}
}

// TestApplyByteBudgetFloorTerminates: a budget below the irreducible floor empties
// every list and terminates (best-effort), reporting FinalBytes above the budget
// rather than spinning.
func TestApplyByteBudgetFloorTerminates(t *testing.T) {
	f := boundFake{Big: repeat("x", 10), Small: repeat("y", 10)}
	m, err := f.bound(responseMinBytesUnreachable)
	if err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	if m == nil {
		t.Fatalf("marker = nil, want a best-effort marker")
	}
	if len(f.Big) != 0 || len(f.Small) != 0 {
		t.Errorf("lists not emptied: big=%d small=%d", len(f.Big), len(f.Small))
	}
	if m.FinalBytes <= m.MaxBytes {
		t.Errorf("FinalBytes = %d, want > MaxBytes %d on a floor breach", m.FinalBytes, m.MaxBytes)
	}
}

// TestApplyByteBudgetDeterministic: the same input yields identical trimming and
// marker ordering across runs (no map-iteration nondeterminism).
func TestApplyByteBudgetDeterministic(t *testing.T) {
	run := func() *SizeBoundFacts {
		f := boundFake{Big: repeat(strings.Repeat("A", 30), 20), Small: repeat("ss", 20)}
		m, err := f.bound(500)
		if err != nil {
			t.Fatalf("ApplyByteBudget: %v", err)
		}
		return m
	}
	a, b := run(), run()
	if mustMarshalStr(t, a) != mustMarshalStr(t, b) {
		t.Errorf("non-deterministic marker:\n a=%s\n b=%s", mustMarshalStr(t, a), mustMarshalStr(t, b))
	}
}

// TestApplyByteBudgetFloorShortCircuits: when the irreducible floor exceeds the
// budget, the bound reaches the emptied terminal state in a constant number of
// marshals instead of draining one element per round — and reproduces the same
// TrimmedBlocks accounting the incremental drain would have. The marshal count is the
// red-first pin (O(items) before the short-circuit); the emptiness, FinalBytes, and
// TrimmedBlocks assertions guard that the fast path stays byte-identical to the drain.
func TestApplyByteBudgetFloorShortCircuits(t *testing.T) {
	const maxBytes = responseMinBytesUnreachable
	f := boundFake{Big: repeat("x", 500), Small: repeat("y", 500)}
	marshals := 0
	m, err := ApplyByteBudget(
		func() ([]byte, error) { marshals++; return json.Marshal(f) },
		func(mk *SizeBoundFacts) { f.Marker = mk },
		maxBytes,
		[]Trimmable{
			sliceUnit("big", &f.Big, &f.BigTrunc),
			sliceUnit("small", &f.Small, &f.SmallTrunc),
		},
	)
	if err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	// A constant number of marshals regardless of the 1000 droppable elements: the
	// initial fit check plus the single floor probe. The pre-fix drain is ~O(items).
	if marshals > 4 {
		t.Errorf("marshals = %d, want a small constant (floor short-circuit, not an O(items) drain)", marshals)
	}
	if m == nil {
		t.Fatalf("marker = nil, want a best-effort marker on a sub-floor budget")
	}
	if len(f.Big) != 0 || len(f.Small) != 0 {
		t.Errorf("lists not emptied: big=%d small=%d", len(f.Big), len(f.Small))
	}
	if m.FinalBytes <= m.MaxBytes {
		t.Errorf("FinalBytes = %d, want > MaxBytes %d on a floor breach", m.FinalBytes, m.MaxBytes)
	}
	// The direct-built overflow marker must carry the same per-block tally the drain
	// would have reached: every element dropped, none remaining.
	seen := map[string]bool{}
	for _, tb := range m.TrimmedBlocks {
		seen[tb.Block] = true
		if tb.Dropped != 500 || tb.Remaining != 0 {
			t.Errorf("%s: dropped=%d remaining=%d, want 500/0", tb.Block, tb.Dropped, tb.Remaining)
		}
	}
	if !seen["big"] || !seen["small"] {
		t.Errorf("TrimmedBlocks = %+v, want entries for both big and small", m.TrimmedBlocks)
	}
}

// TestApplyByteBudgetRestoreResetsDropAllPollution: on the floor-fits branch the bound
// empties every list to probe the floor (setting each truncated flag), then restores.
// A unit the incremental loop never re-drops must come back with its truncated flag
// reset to the value it had before the bound — false here. This is the polarity that
// regresses: a Restore that skips the flag would leave the drop-all pollution and
// report a complete list as truncated. (A wants-true assertion cannot catch it — see
// the companion below.)
func TestApplyByteBudgetRestoreResetsDropAllPollution(t *testing.T) {
	// Big is heavy and long; Small is tiny and starts untruncated. Draining Big alone
	// reaches the budget while Big is still larger than Small, so Small never becomes
	// the largest unit and is left fully intact.
	f := boundFake{Big: repeat(strings.Repeat("A", 50), 20), Small: repeat("s", 3)}
	if _, err := f.bound(700); err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	if len(f.Small) != 3 || f.SmallTrunc {
		t.Errorf("small untouched-unit regressed: len=%d trunc=%v, want len=3 trunc=false", len(f.Small), f.SmallTrunc)
	}
	if len(f.Big) >= 20 || !f.BigTrunc {
		t.Errorf("big should have been trimmed: len=%d trunc=%v", len(f.Big), f.BigTrunc)
	}
}

// TestApplyByteBudgetRestorePreservesPriorTruncation: the dual-source companion. A unit
// that carried truncated == true before the bound ran (the parser count-cap case) and
// is left intact by the incremental loop must keep truncated == true — Restore writes
// the original value, not a constant. Catches a Restore that force-resets to false.
func TestApplyByteBudgetRestorePreservesPriorTruncation(t *testing.T) {
	f := boundFake{Big: repeat(strings.Repeat("A", 50), 20), Small: repeat("s", 3), SmallTrunc: true}
	if _, err := f.bound(700); err != nil {
		t.Fatalf("ApplyByteBudget: %v", err)
	}
	if len(f.Small) != 3 || !f.SmallTrunc {
		t.Errorf("small prior-truncation not preserved: len=%d trunc=%v, want len=3 trunc=true", len(f.Small), f.SmallTrunc)
	}
}

// responseMinBytesUnreachable is a budget below any composite skeleton, used to
// drive the best-effort floor path.
const responseMinBytesUnreachable = 10

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustMarshalStr(t *testing.T, v any) string { return string(mustMarshal(t, v)) }

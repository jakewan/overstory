package reduce

import "encoding/json"

// SizeBoundFacts is the top-level marker a composite reduction sets when it had to
// trim detail item-lists to keep the total response within a byte budget — the
// reliability bound that stops a large repo's response from exceeding the MCP
// client's tool-result token cap and failing. It is absent (the field is a nil
// pointer with omitempty) when the response fit the budget untouched, so normal
// responses are byte-identical to an unbounded server.
//
// FinalBytes is the measured byte length of one serialization of the bounded facts
// and is an upper bound, not an exact wire figure: the SDK emits the facts twice
// (once in structuredContent, once in a back-compat text block), so the wire
// payload is roughly twice FinalBytes, and FinalBytes is measured with a
// fixed-width placeholder for its own digits so the real serialization can only be
// smaller. When FinalBytes exceeds MaxBytes the bound was best-effort: the
// irreducible floor (counts, summaries, the open-issue set, and this marker, with
// every trimmable list emptied) alone exceeded the budget.
type SizeBoundFacts struct {
	MaxBytes      int            `json:"maxBytes"`
	FinalBytes    int            `json:"finalBytes"`
	TrimmedBlocks []TrimmedBlock `json:"trimmedBlocks"`
}

// TrimmedBlock records how much of one block's detail list the size bound removed.
// Dropped and Remaining count the block's natural list element — items for the flat
// list blocks (staleness, deferred, quality, hygiene signals, open PRs,
// recommendations), whole groups for overlap and cross-reference, and the member
// lists nested within a kept entry: the open members within each milestone
// (project_summary) or the members within each track (milestone_tracks), the entry
// itself preserved. The block's own count fields (e.g. DeferredCount, GroupCount)
// remain the authoritative totals; this only reconciles the shown list length, which
// the per-block limit no longer predicts once a bound is present.
type TrimmedBlock struct {
	Block     string `json:"block"`
	Dropped   int    `json:"dropped"`
	Remaining int    `json:"remaining"`
}

// Trimmable is one block's handle for the size bound: its name, the current
// serialized byte size and remaining unit count of its detail list, a Drop that
// removes one tail unit and marks the block truncated, and a Restore that rewinds
// the list to its pre-Drop length and its truncated flag to the value it carried
// before the bound ran. The caller (a tool handler) builds these over the assembled
// facts; closing over the same live facts value the measure closure marshals keeps a
// single source of truth — there is no copy that could drift from what is serialized.
//
// All four closures are required and must be non-nil. ApplyByteBudget calls Restore
// unconditionally on the floor-fits branch (to undo the floor probe), so a nil Restore
// panics there. That is deliberate: nil-guarding Restore to a no-op would instead leave
// the probed lists emptied and silently over-trim the result — worse than a loud panic.
// A Drop must reslice down only (never reallocate or zero), so the backing array still
// holds the tail elements a later Restore reslices back into view.
type Trimmable struct {
	Block     string
	Size      func() int
	Remaining func() int
	Drop      func()
	Restore   func()
}

// finalBytesSentinel is a fixed, generously wide placeholder for the marker's own
// FinalBytes field during the trim loop. It has more digits than any realistic
// FinalBytes, so when the real (smaller) value replaces it the serialization can
// only shrink — making the measured size a true upper bound on the delivered one.
const finalBytesSentinel = 2_000_000_000

// ApplyByteBudget bounds a composite response to maxBytes by greedily trimming the
// detail lists in units. measure marshals the live facts; setMarker installs (or
// clears, on nil) this marker onto those same facts so the marker's own bytes are
// inside every measurement. If the facts already fit, it clears any marker and
// returns nil.
//
// Otherwise it probes the floor first: it empties every list once and measures the
// fully-trimmed response a single time. When even that floor overflows the budget —
// the untrimmable content (counts, summaries, verbatim descriptions) alone exceeds
// maxBytes — the emptied state is the terminal answer, so it returns directly with a
// best-effort marker. This short-circuits the drain that would otherwise empty every
// element one at a time (O(items) marshals) on exactly the large, prose-dominated
// input the bound exists to handle. When the floor fits, it restores every list and
// trims one unit per round from the block whose current serialized list is largest in
// bytes (ties broken by unit order, so the result is deterministic), re-measuring each
// round, until the response fits. Trimming is balanced rather than richest-first:
// dropping from the current-largest each round equalizes byte spend across blocks
// instead of gutting the heaviest block (e.g. deferred) before touching thin ones. The
// returned marker is also installed via setMarker, so the caller need only check the
// error.
func ApplyByteBudget(measure func() ([]byte, error), setMarker func(*SizeBoundFacts), maxBytes int, units []Trimmable) (*SizeBoundFacts, error) {
	setMarker(nil)
	b, err := measure()
	if err != nil {
		return nil, err
	}
	if len(b) <= maxBytes {
		return nil, nil
	}

	// Empty every list with cheap reslices (no marshals) to measure the floor once.
	// The per-drop tally is kept so a best-effort marker reproduces the same
	// TrimmedBlocks the incremental drain would have reported.
	dropped := make(map[string]int, len(units))
	for i := range units {
		for units[i].Remaining() > 0 {
			units[i].Drop()
			dropped[units[i].Block]++
		}
	}
	setMarker(buildMarker(maxBytes, finalBytesSentinel, units, dropped))
	floor, err := measure()
	if err != nil {
		return nil, err
	}
	if len(floor) > maxBytes {
		// floor was measured with the wide FinalBytes sentinel, so it is an upper bound
		// on the delivered size once the real value (fewer digits) is written back.
		m := buildMarker(maxBytes, len(floor), units, dropped)
		setMarker(m)
		return m, nil
	}

	// The floor fits: the response has a balanced trim point between full and empty.
	// Undo the probe and trim incrementally to find it.
	for i := range units {
		units[i].Restore()
	}
	dropped = make(map[string]int, len(units))
	for {
		setMarker(buildMarker(maxBytes, finalBytesSentinel, units, dropped))
		b, err = measure()
		if err != nil {
			return nil, err
		}
		if len(b) <= maxBytes {
			break
		}
		i := largestUnit(units)
		if i < 0 {
			break // defensive: the fit break above triggers first once the floor fits
		}
		units[i].Drop()
		dropped[units[i].Block]++
	}

	// len(b) was measured with the wide FinalBytes sentinel, so it is an upper bound
	// on the delivered size once the real value (fewer digits) is written back.
	m := buildMarker(maxBytes, len(b), units, dropped)
	setMarker(m)
	return m, nil
}

// buildMarker assembles the marker from the current per-block drop tally, in unit
// order so the output is deterministic. TrimmedBlocks is non-nil so it serializes
// as [] rather than null.
func buildMarker(maxBytes, finalBytes int, units []Trimmable, dropped map[string]int) *SizeBoundFacts {
	tbs := make([]TrimmedBlock, 0, len(dropped))
	for _, u := range units {
		if d := dropped[u.Block]; d > 0 {
			tbs = append(tbs, TrimmedBlock{Block: u.Block, Dropped: d, Remaining: u.Remaining()})
		}
	}
	return &SizeBoundFacts{MaxBytes: maxBytes, FinalBytes: finalBytes, TrimmedBlocks: tbs}
}

// largestUnit returns the index of the unit with the largest current serialized
// list, among those with units left to drop, or -1 when none remain. The strict
// comparison keeps the earliest unit on a tie, so trimming is order-deterministic.
func largestUnit(units []Trimmable) int {
	best := -1
	bestSize := 0
	for i := range units {
		if units[i].Remaining() <= 0 {
			continue
		}
		if s := units[i].Size(); best < 0 || s > bestSize {
			best, bestSize = i, s
		}
	}
	return best
}

// JSONLen is the serialized byte size of v, used by handlers to report a block
// list's current byte contribution to a Trimmable. A marshal error yields 0, which
// only deprioritizes that unit for trimming — the loop still terminates via the
// other units or the empty-floor break.
func JSONLen(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

package reduce

import (
	"sort"

	"github.com/jakewan/overstory/internal/github"
)

// OpenBlockerNumbers projects an issue's native blocked-by edges to the ascending,
// distinct numbers of the blockers still open — the authoritative "what still gates
// this issue" signal. Closed blockers are dropped (a closed blocker no longer
// blocks); the cross-repository drop already happened in the fetch layer, so every
// ref here is a same-repo issue. The result is non-nil even when empty, so a
// reduction embedding it serializes [] rather than null — matching the bodyRefs
// convention the two dependency signals share.
//
// This is the single open-blocker projection both the backlog and summary
// reductions call, so the authoritative dependency signal reads identically on
// both tools.
func OpenBlockerNumbers(refs []github.BlockedByRef) []int {
	seen := make(map[int]bool, len(refs))
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		if !r.Open || seen[r.Number] {
			continue
		}
		seen[r.Number] = true
		out = append(out, r.Number)
	}
	sort.Ints(out)
	return out
}

package reduce

import (
	"sort"

	"github.com/jakewan/overstory/internal/github"
)

// OpenDependencyNumbers projects an issue's native dependency edges — in either
// direction — to the ascending, distinct numbers of the referenced issues still
// open. For a blocked-by set it is "what still gates this issue"; for a blocking
// set it is "which still-open issues this one gates". Closed edges are dropped (a
// closed issue no longer gates either way); the cross-repository drop already
// happened in the fetch layer, so every ref here is a same-repo issue. The result
// is non-nil even when empty, so a reduction embedding it serializes [] rather than
// null — matching the bodyRefs convention the dependency signals share.
//
// This is the single open-edge projection both the backlog and summary reductions
// call for both directions, so the authoritative dependency signal reads identically
// on both tools.
func OpenDependencyNumbers(refs []github.DependencyRef) []int {
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

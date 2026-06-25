package reduce

import "sort"

// OpenIssueSetFacts is the set of open issue numbers in the fetched window — the
// resolvable surface for stated bodyRefs. Same-repo, open, ISSUES ONLY (PRs share
// the number space and are excluded). FetchTruncated mirrors the per-block flag by
// design (both derive len(issues) < totalOpen); duplicated for block
// self-containment so a caller resolving refs against Numbers reads the coverage
// caveat from the same block.
//
// Presence gates; absence does not clear: a ref in Numbers names a live open issue
// in this repo (a gate candidate to verify), but a ref absent from Numbers is not
// proof of resolution — it may be a closed issue, an open PR, a cross-repo
// reference, or (when FetchTruncated) an open issue beyond the window.
type OpenIssueSetFacts struct {
	Numbers        []int `json:"numbers"`        // ascending, distinct, non-nil; the FULL fetched window, never limit-capped
	FetchTruncated bool  `json:"fetchTruncated"` // true when the window didn't cover every open issue (Numbers is a floor)
}

// NewOpenIssueSet builds the open-issue set from the fetched issue numbers: the
// result's Numbers is ascending, distinct, and non-nil (so it serializes [] rather
// than null even when empty). It takes a plain []int rather than the github issue
// shape so the reduce layer stays stdlib-only — the handler extracts .Number and
// passes the slice. fetchTruncated is the window-coverage flag the caller derives
// (len(issues) < totalOpen) and is carried through unchanged.
func NewOpenIssueSet(numbers []int, fetchTruncated bool) OpenIssueSetFacts {
	seen := make(map[int]bool, len(numbers))
	out := make([]int, 0, len(numbers))
	for _, n := range numbers {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return OpenIssueSetFacts{Numbers: out, FetchTruncated: fetchTruncated}
}

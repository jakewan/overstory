package reduce

import (
	"regexp"
	"sort"
	"strconv"
)

var (
	// issueRefRe matches an issue reference's leading `#` and digits. Pull-request
	// references are excluded separately by inspecting the preceding text — this
	// pattern alone cannot tell `#5` from `PR #5`.
	issueRefRe = regexp.MustCompile(`#(\d+)`)
	// prContextRe marks a pull-request reference to exclude by the text immediately
	// before the `#`: a word-boundaried `PR`/`PR#`/`PR ` (so "expr" does not match)
	// or a `pull/` path segment.
	prContextRe = regexp.MustCompile(`(?i)(\bpr\s*|pull/)$`)
)

// IssueRef is one #N reference found in text: its issue Number and the byte
// Start of the '#', so a caller can inspect the text before the reference (for
// strikethrough or checkbox decoration) without re-scanning.
type IssueRef struct {
	Number int
	Start  int
}

// IssueRefMatches returns the #N references in text in appearance order, with
// pull-request references (`PR #5`, `pull/5`) excluded and any number too large
// for int skipped. It does not dedup: callers that need appearance order and
// per-reference context (e.g. milestone tracks, which decorate each member) read
// the raw sequence. IssueRefs is the deduped, sorted convenience over it.
func IssueRefMatches(text string) []IssueRef {
	locs := issueRefRe.FindAllStringSubmatchIndex(text, -1)
	out := make([]IssueRef, 0, len(locs))
	for _, loc := range locs {
		refStart, numStart, numEnd := loc[0], loc[2], loc[3]
		if prContextRe.MatchString(text[:refStart]) {
			continue
		}
		num, err := strconv.Atoi(text[numStart:numEnd])
		if err != nil {
			continue // a number too large for int is not a usable reference
		}
		out = append(out, IssueRef{Number: num, Start: refStart})
	}
	return out
}

// IssueRefs is IssueRefMatches deduped and ascending-sorted: the distinct #N
// references in text, PR-excluded. It returns a non-nil empty slice when there
// are none, so a caller embedding it serializes [] rather than null.
func IssueRefs(text string) []int {
	matches := IssueRefMatches(text)
	seen := make(map[int]bool, len(matches))
	out := make([]int, 0, len(matches))
	for _, m := range matches {
		if seen[m.Number] {
			continue
		}
		seen[m.Number] = true
		out = append(out, m.Number)
	}
	sort.Ints(out)
	return out
}

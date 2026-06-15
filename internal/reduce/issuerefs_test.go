package reduce

import "testing"

func TestIssueRefs(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []int
	}{
		{"multiple deduped and sorted", "blocks #5 and #3, also #5 again", []int{3, 5}},
		{"pull-request reference excluded", "needs PR #5 before #6 lands", []int{6}},
		{"pull path excluded", "superseded by pull/7 — see #8", []int{8}},
		// The `\bpr` word boundary means the "pr" inside "expr" is not a PR prefix,
		// so the trailing reference survives.
		{"pr inside a word is not a prefix", "expr#5", []int{5}},
		{"no references yields empty (non-nil) slice", "nothing to see here", []int{}},
		// A number too large for int (the original strconv.Atoi failure path) is
		// skipped, not panicked on.
		{"overflowing number is skipped", "tracking #99999999999999999999999 here", []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := IssueRefs(tc.text)
			if got == nil {
				t.Fatalf("IssueRefs(%q) = nil, want non-nil slice", tc.text)
			}
			if !equalInts(got, tc.want) {
				t.Errorf("IssueRefs(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestIssueRefMatchesPreservesOrderWithoutDedup pins the rich variant the
// milestone-tracks parser needs: appearance order, no dedup, and a Start byte
// index that points at the '#'.
func TestIssueRefMatchesPreservesOrderWithoutDedup(t *testing.T) {
	text := "#3 then #1 then #3"
	got := IssueRefMatches(text)
	wantNums := []int{3, 1, 3}
	if len(got) != len(wantNums) {
		t.Fatalf("IssueRefMatches(%q) returned %d refs, want %d", text, len(got), len(wantNums))
	}
	for i, w := range wantNums {
		if got[i].Number != w {
			t.Errorf("ref[%d].Number = %d, want %d", i, got[i].Number, w)
		}
		if text[got[i].Start] != '#' {
			t.Errorf("ref[%d].Start = %d points at %q, want '#'", i, got[i].Start, text[got[i].Start])
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

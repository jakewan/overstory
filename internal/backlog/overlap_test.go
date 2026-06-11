package backlog

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// titled builds an open issue with a given title for the overlap reduction,
// which keys off the title and is time-independent.
func titled(num int, title string) github.Issue {
	return github.Issue{Number: num, Title: title, URL: fmt.Sprintf("https://example.com/%d", num)}
}

func params(threshold float64) OverlapParams { return OverlapParams{TitleThreshold: threshold} }

func TestNormalizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Fix: the Login!", "fix the login"},
		{"  Hello   World  ", "hello world"},
		{"already-normalized v2", "already normalized v2"},
		{"🎉🔥", ""},     // emoji-only → empty (un-linkable)
		{"???", ""},    // punctuation-only → empty
		{"日本語", "日本語"}, // CJK letters survive normalization
	}
	for _, c := range cases {
		if got := normalizeTitle(c.in); got != c.want {
			t.Errorf("normalizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDice(t *testing.T) {
	same := trigrams(normalizeTitle("cache timeout error"))
	if got := dice(same, same); got != 1.0 {
		t.Errorf("dice(x,x) = %g, want 1.0", got)
	}
	// Empty multiset on either side scores 0 — the C1 guard against the metric's
	// degenerate empty-vs-empty = 1.
	if got := dice(map[string]int{}, map[string]int{}); got != 0 {
		t.Errorf("dice(empty,empty) = %g, want 0", got)
	}
	if got := dice(same, map[string]int{}); got != 0 {
		t.Errorf("dice(x,empty) = %g, want 0", got)
	}
	// Disjoint titles score 0.
	if got := dice(trigrams("aaa"), trigrams("zzz")); got != 0 {
		t.Errorf("dice(disjoint) = %g, want 0", got)
	}
	// A one-character difference on a long title is highly similar but not identical.
	near := dice(trigrams(normalizeTitle("cache timeout error")), trigrams(normalizeTitle("cache timeout errors")))
	if near <= 0.9 || near >= 1.0 {
		t.Errorf("dice(near-identical) = %g, want in (0.9,1.0)", near)
	}
}

func TestReduceOverlapGroupsSimilarTitlesWithEvidence(t *testing.T) {
	issues := []github.Issue{
		titled(1, "Fix login bug"),
		titled(2, "Fix login bug."), // normalizes identically → linked
		titled(3, "Add dark mode"),  // unrelated
	}
	facts := ReduceOverlap(issues, 3, params(0.5), 20)

	if facts.GroupCount != 1 {
		t.Fatalf("GroupCount = %d, want 1; groups=%+v", facts.GroupCount, facts.Groups)
	}
	g := facts.Groups[0]
	if len(g.Issues) != 2 || g.Issues[0].Number != 1 || g.Issues[1].Number != 2 {
		t.Errorf("group members = %+v, want issues 1 and 2", g.Issues)
	}
	wantTokens := []string{"bug", "fix", "login"}
	if !reflect.DeepEqual(g.SharedTokens, wantTokens) {
		t.Errorf("SharedTokens = %v, want %v", g.SharedTokens, wantTokens)
	}
	if facts.OverlappingCount != 2 || facts.LargestGroupSize != 2 {
		t.Errorf("OverlappingCount=%d LargestGroupSize=%d, want 2 and 2", facts.OverlappingCount, facts.LargestGroupSize)
	}
}

func TestReduceOverlapThresholdGatesTrigramPath(t *testing.T) {
	// Near-identical (not equal) titles: linked at a moderate threshold, not at an
	// exact-match-only threshold — exercising the trigram metric, not the
	// exact-match short-circuit.
	issues := []github.Issue{titled(1, "cache timeout error"), titled(2, "cache timeout errors")}

	if g := ReduceOverlap(issues, 2, params(0.5), 20).GroupCount; g != 1 {
		t.Errorf("at threshold 0.5: GroupCount = %d, want 1", g)
	}
	if g := ReduceOverlap(issues, 2, params(1.0), 20).GroupCount; g != 0 {
		t.Errorf("at threshold 1.0 (exact only): GroupCount = %d, want 0", g)
	}
}

func TestReduceOverlapThresholdZeroDisables(t *testing.T) {
	// Identical titles that would otherwise link are not grouped when the reduction
	// is disabled with a 0 threshold.
	issues := []github.Issue{titled(1, "same title"), titled(2, "same title")}
	facts := ReduceOverlap(issues, 2, params(0), 20)
	if facts.GroupCount != 0 || len(facts.Groups) != 0 {
		t.Errorf("threshold 0: GroupCount=%d Groups=%+v, want disabled", facts.GroupCount, facts.Groups)
	}
}

func TestReduceOverlapEmptyTitlesDoNotGroup(t *testing.T) {
	// C1: titles that normalize to empty (emoji/punctuation-only) must not link to
	// each other, despite Sørensen–Dice scoring empty-vs-empty as 1.0.
	issues := []github.Issue{titled(1, "🎉"), titled(2, "🔥"), titled(3, "???")}
	if g := ReduceOverlap(issues, 3, params(0.5), 20).GroupCount; g != 0 {
		t.Errorf("empty-normalized titles: GroupCount = %d, want 0", g)
	}
}

func TestReduceOverlapIdenticalShortTitlesGroup(t *testing.T) {
	// C2: byte-identical titles shorter than a trigram still link, via the
	// exact-match short-circuit (their trigram score would be 0.0).
	issues := []github.Issue{titled(1, "CI"), titled(2, "ci")}
	facts := ReduceOverlap(issues, 2, params(0.5), 20)
	if facts.GroupCount != 1 {
		t.Fatalf("identical short titles: GroupCount = %d, want 1", facts.GroupCount)
	}
}

func TestReduceOverlapExactOpenAndFetchTruncation(t *testing.T) {
	issues := []github.Issue{titled(1, "x"), titled(2, "y")}
	facts := ReduceOverlap(issues, 500, params(0.5), 20)
	if facts.OpenIssueCount != 500 {
		t.Errorf("OpenIssueCount = %d, want 500 (exact, not fetched count)", facts.OpenIssueCount)
	}
	if facts.FetchedCount != 2 || !facts.FetchTruncated {
		t.Errorf("FetchedCount=%d FetchTruncated=%v, want 2 and true", facts.FetchedCount, facts.FetchTruncated)
	}
}

func TestReduceOverlapListTruncationCapsGroupsButNotLargest(t *testing.T) {
	// Two groups found (sizes 3 and 2); a list limit of 1 caps the listed groups but
	// the totals — GroupCount, OverlappingCount, LargestGroupSize — stay exact, so a
	// runaway cluster stays visible even when truncated off the list.
	issues := []github.Issue{
		titled(1, "alpha beta"), titled(2, "alpha beta"), titled(3, "alpha beta"),
		titled(4, "gamma delta"), titled(5, "gamma delta"),
	}
	facts := ReduceOverlap(issues, 5, params(0.5), 1)

	if facts.GroupCount != 2 {
		t.Errorf("GroupCount = %d, want 2 (total, not capped)", facts.GroupCount)
	}
	if !facts.ListTruncated || len(facts.Groups) != 1 {
		t.Errorf("ListTruncated=%v len(Groups)=%d, want true and 1", facts.ListTruncated, len(facts.Groups))
	}
	if len(facts.Groups[0].Issues) != 3 {
		t.Errorf("listed group size = %d, want 3 (largest first)", len(facts.Groups[0].Issues))
	}
	if facts.LargestGroupSize != 3 || facts.OverlappingCount != 5 {
		t.Errorf("LargestGroupSize=%d OverlappingCount=%d, want 3 and 5", facts.LargestGroupSize, facts.OverlappingCount)
	}
}

func TestReduceOverlapNoIssuesAndSingleton(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issues []github.Issue
	}{
		{"empty", nil},
		{"singleton", []github.Issue{titled(1, "only one")}},
	} {
		facts := ReduceOverlap(tc.issues, len(tc.issues), params(0.5), 20)
		if facts.GroupCount != 0 {
			t.Errorf("%s: GroupCount = %d, want 0", tc.name, facts.GroupCount)
		}
		if facts.Groups == nil {
			t.Errorf("%s: Groups is nil, want non-nil so it serializes as []", tc.name)
		}
	}
}

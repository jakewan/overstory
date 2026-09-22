package reduce

import (
	"reflect"
	"testing"
)

// selfRepo is the repository the parser tests survey, so a reference qualified
// with it (or its owner alone) reads as local.
const selfRepo = "acme/widgets"

func TestBodyRefs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		text         string
		exclude      int
		wantLocal    []int
		wantExternal []ForeignRef
	}{
		{"multiple deduped and sorted", "blocks #5 and #3, also #5 again", 0, []int{3, 5}, []ForeignRef{}},
		{"pull-request reference excluded", "needs PR #5 before #6 lands", 0, []int{6}, []ForeignRef{}},
		{"unspaced pull-request reference excluded", "needs PR#5 before #6 lands", 0, []int{6}, []ForeignRef{}},
		{"pull/ path segment before a ref is excluded", "superseded by pull/#7 — see #8", 0, []int{8}, []ForeignRef{}},
		{"no references yields empty (non-nil) slices", "nothing to see here", 0, []int{}, []ForeignRef{}},
		// A number too large for int (the original strconv.Atoi failure path) is
		// skipped, not panicked on.
		{"overflowing number is skipped", "tracking #99999999999999999999999 here", 0, []int{}, []ForeignRef{}},
		{"excludes the given number", "blocks #5 and #3, also #5 again", 5, []int{3}, []ForeignRef{}},
		{"self-only reference empties to non-nil slice", "depends on #7 only", 7, []int{}, []ForeignRef{}},

		// Repository-qualified references name another repository's issue, never a
		// local one — the #154 defect.
		{"qualified reference is foreign", "blocked by other/lib#87650", 0, []int{}, []ForeignRef{{Repo: "other/lib", Number: 87650}}},
		{"qualified at line start", "other/lib#5 first", 0, []int{}, []ForeignRef{{Repo: "other/lib", Number: 5}}},
		{"qualified inside parentheses", "(other/lib#5)", 0, []int{}, []ForeignRef{{Repo: "other/lib", Number: 5}}},
		{"qualified inside brackets", "[other/lib#5]", 0, []int{}, []ForeignRef{{Repo: "other/lib", Number: 5}}},
		{"qualified before trailing punctuation", "see other/lib#5, other/lib#6. and other/lib#7)", 0, []int{},
			[]ForeignRef{{Repo: "other/lib", Number: 5}, {Repo: "other/lib", Number: 6}, {Repo: "other/lib", Number: 7}}},
		{"hyphenated owner kept whole", "octo-org/repo#5", 0, []int{}, []ForeignRef{{Repo: "octo-org/repo", Number: 5}}},
		{"dotted and underscored repo name", "jlord/sheetsee.js#26 and a/b_c#1", 0, []int{},
			[]ForeignRef{{Repo: "a/b_c", Number: 1}, {Repo: "jlord/sheetsee.js", Number: 26}}},
		// GitHub links the last owner/repo pair before the '#', even behind a path.
		{"last owner/repo pair before the # wins", "x/anthropics/claude-code#1", 0, []int{},
			[]ForeignRef{{Repo: "anthropics/claude-code", Number: 1}}},
		{"deeper path still takes the last pair", "a/b/c#5", 0, []int{}, []ForeignRef{{Repo: "b/c", Number: 5}}},
		// A repository whose name ends in "pr" is not pull-request context: the check
		// reads the text before the qualifier.
		{"repo named like a PR marker is not excluded", "acme-lib/my-pr#5 and acme-lib/pr#6", 0, []int{},
			[]ForeignRef{{Repo: "acme-lib/my-pr", Number: 5}, {Repo: "acme-lib/pr", Number: 6}}},
		{"PR marker before a qualified ref still excludes it", "PR other/lib#5", 0, []int{}, []ForeignRef{}},
		{"case duplicates dedup to the first spelling", "other/lib#5 then Other/Lib#5", 0, []int{},
			[]ForeignRef{{Repo: "other/lib", Number: 5}}},

		// A reference qualified with the surveyed repository is local, in any casing,
		// as GitHub renders it.
		{"self-qualified is local", "acme/widgets#12", 0, []int{12}, []ForeignRef{}},
		{"self-qualified matches case-insensitively", "Acme/Widgets#12", 0, []int{12}, []ForeignRef{}},
		{"self-qualified own number is excluded", "acme/widgets#7", 7, []int{}, []ForeignRef{}},

		// GitHub's owner-only form addresses that owner's fork of this repository,
		// found by owner whatever the fork is named — so it carries the owner, never a
		// guessed repository name. The surveyed owner's own form is local.
		{"fork owner form is foreign", "see bob#40", 0, []int{}, []ForeignRef{{ForkOwner: "bob", Number: 40}}},
		{"surveyed owner form is local", "see acme#40 and ACME#41", 0, []int{40, 41}, []ForeignRef{}},
		// Text alone cannot tell a word from a fork owner, so a word glued to the '#'
		// is read as one rather than as a local reference it never linked to.
		{"word glued to the # reads as a fork owner", "expr#5", 0, []int{}, []ForeignRef{{ForkOwner: "expr", Number: 5}}},
		// A hyphen cannot start an owner name, so a dash before the '#' leaves it local.
		{"leading hyphen is not an owner", "item -#5", 0, []int{5}, []ForeignRef{}},
		// Nor can a hyphen end one, and GitHub links the #N after it as local.
		{"trailing hyphen is not an owner", "see re-#12 and bob-#13", 0, []int{12, 13}, []ForeignRef{}},
		{"qualified entries order by qualifier then number", "zed/a#1 bob#9 bob#2 alpha/b#3", 0, []int{},
			[]ForeignRef{{Repo: "alpha/b", Number: 3}, {ForkOwner: "bob", Number: 2}, {ForkOwner: "bob", Number: 9}, {Repo: "zed/a", Number: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, external := BodyRefs(tc.text, selfRepo, tc.exclude)
			if local == nil || external == nil {
				t.Fatalf("BodyRefs(%q) = %v, %v; want non-nil slices", tc.text, local, external)
			}
			if !reflect.DeepEqual(local, tc.wantLocal) {
				t.Errorf("BodyRefs(%q) local = %v, want %v", tc.text, local, tc.wantLocal)
			}
			if !reflect.DeepEqual(external, tc.wantExternal) {
				t.Errorf("BodyRefs(%q) external = %+v, want %+v", tc.text, external, tc.wantExternal)
			}
		})
	}
}

// TestIssueRefMatchesPreservesOrderWithoutDedup pins the rich variant the
// milestone-tracks parser needs: appearance order, no dedup, and a Start byte
// index at the reference's first byte — its qualifier when it has one — so text
// read before Start is the text before the whole reference.
func TestIssueRefMatchesPreservesOrderWithoutDedup(t *testing.T) {
	text := "#3 then other/lib#1 then #3 then bob#2"
	got := IssueRefMatches(text, selfRepo)
	want := []IssueRef{
		{Number: 3, Start: 0},
		{Number: 1, Start: 8, Foreign: &ForeignRef{Repo: "other/lib", Number: 1}},
		{Number: 3, Start: 25},
		{Number: 2, Start: 33, Foreign: &ForeignRef{ForkOwner: "bob", Number: 2}},
	}
	if len(got) != len(want) {
		t.Fatalf("IssueRefMatches(%q) returned %d refs, want %d", text, len(got), len(want))
	}
	for i, w := range want {
		if !reflect.DeepEqual(got[i], w) {
			t.Errorf("ref[%d] = %+v (foreign %+v), want %+v (foreign %+v)", i, got[i], got[i].Foreign, w, w.Foreign)
		}
	}
}

// TestIssueRefMatchesWithoutSelfTreatsEveryQualifierAsForeign pins the empty-self
// contract: with no surveyed repository to compare against, nothing qualified can
// be shown local, so every qualified reference stays foreign.
func TestIssueRefMatchesWithoutSelfTreatsEveryQualifierAsForeign(t *testing.T) {
	got := IssueRefMatches("acme/widgets#1 acme#2 #3", "")
	if len(got) != 3 || got[0].Foreign == nil || got[1].Foreign == nil || got[2].Foreign != nil {
		t.Errorf("IssueRefMatches with empty self = %+v, want foreign, foreign, local", got)
	}
}

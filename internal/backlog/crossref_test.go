package backlog

import (
	"fmt"
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// crossRef builds an open issue for the cross-reference reduction. referencedBy is
// the set of issue numbers that reference this one (the incoming edges), so a
// directed edge runs from each to num.
func crossRef(num int, referencedBy ...int) github.Issue {
	return github.Issue{
		Number:       num,
		Title:        fmt.Sprintf("issue %d", num),
		URL:          fmt.Sprintf("https://example.com/%d", num),
		ReferencedBy: referencedBy,
	}
}

func TestReduceCrossRefGroupsMutualReferenceWithDirectedEvidence(t *testing.T) {
	issues := []github.Issue{
		crossRef(1, 2), // #1 is referenced by #2
		crossRef(2),
		crossRef(3), // unreferenced
	}
	facts := ReduceCrossRef(issues, 3, 20)

	if facts.GroupCount != 1 {
		t.Fatalf("GroupCount = %d, want 1; groups=%+v", facts.GroupCount, facts.Groups)
	}
	g := facts.Groups[0]
	if len(g.Issues) != 2 || g.Issues[0].Number != 1 || g.Issues[1].Number != 2 {
		t.Errorf("members = %+v, want issues 1 and 2", g.Issues)
	}
	if len(g.References) != 1 || g.References[0] != (CrossRef{From: 2, To: 1}) {
		t.Errorf("References = %+v, want one edge 2->1", g.References)
	}
	if facts.LinkedCount != 2 || facts.LargestGroupSize != 2 {
		t.Errorf("LinkedCount=%d LargestGroupSize=%d, want 2 and 2", facts.LinkedCount, facts.LargestGroupSize)
	}
}

func TestReduceCrossRefTransitiveChain(t *testing.T) {
	// #1←#2 and #2←#3 chain into one component even though #1 and #3 don't link
	// directly — the spine's transitivity.
	issues := []github.Issue{crossRef(1, 2), crossRef(2, 3), crossRef(3)}
	facts := ReduceCrossRef(issues, 3, 20)

	if facts.GroupCount != 1 || len(facts.Groups[0].Issues) != 3 {
		t.Fatalf("want one group of 3; got %+v", facts.Groups)
	}
	refs := facts.Groups[0].References
	want := []CrossRef{{From: 2, To: 1}, {From: 3, To: 2}}
	if len(refs) != len(want) {
		t.Fatalf("References = %+v, want %+v", refs, want)
	}
	for i, w := range want {
		if refs[i] != w {
			t.Errorf("References[%d] = %+v, want %+v", i, refs[i], w)
		}
	}
}

func TestReduceCrossRefOutOfWindowReferenceFormsNoEdge(t *testing.T) {
	// #1 is referenced by #99, which isn't in the fetched window: no node to link.
	facts := ReduceCrossRef([]github.Issue{crossRef(1, 99), crossRef(2)}, 2, 20)
	if facts.GroupCount != 0 {
		t.Errorf("GroupCount = %d, want 0 (referencer outside window)", facts.GroupCount)
	}
}

func TestReduceCrossRefSelfReferenceIgnored(t *testing.T) {
	// A self-reference (matched by number, never slice position) forms no edge.
	facts := ReduceCrossRef([]github.Issue{crossRef(1, 1), crossRef(2)}, 2, 20)
	if facts.GroupCount != 0 {
		t.Errorf("GroupCount = %d, want 0 (self-reference)", facts.GroupCount)
	}
}

func TestReduceCrossRefLargestGroupFirst(t *testing.T) {
	issues := []github.Issue{
		crossRef(5, 6), // 2-member group
		crossRef(6),
		crossRef(1, 2), // 3-member chain
		crossRef(2, 3),
		crossRef(3),
		crossRef(9), // singleton, excluded
	}
	facts := ReduceCrossRef(issues, 6, 20)

	if facts.GroupCount != 2 {
		t.Fatalf("GroupCount = %d, want 2; groups=%+v", facts.GroupCount, facts.Groups)
	}
	if len(facts.Groups[0].Issues) != 3 || len(facts.Groups[1].Issues) != 2 {
		t.Errorf("groups not largest-first: %+v", facts.Groups)
	}
	if facts.LargestGroupSize != 3 || facts.LinkedCount != 5 {
		t.Errorf("LargestGroupSize=%d LinkedCount=%d, want 3 and 5", facts.LargestGroupSize, facts.LinkedCount)
	}
}

func TestReduceCrossRefListTruncationCapsGroupsNotLargest(t *testing.T) {
	issues := []github.Issue{
		crossRef(1, 2), crossRef(2, 3), crossRef(3), // 3-member group
		crossRef(5, 6), crossRef(6), // 2-member group
	}
	facts := ReduceCrossRef(issues, 5, 1)

	if !facts.ListTruncated {
		t.Error("ListTruncated = false, want true")
	}
	if facts.GroupCount != 2 {
		t.Errorf("GroupCount = %d, want 2 (untruncated count)", facts.GroupCount)
	}
	if len(facts.Groups) != 1 {
		t.Errorf("listed groups = %d, want 1 (capped)", len(facts.Groups))
	}
	if facts.LargestGroupSize != 3 {
		t.Errorf("LargestGroupSize = %d, want 3 (preserved across truncation)", facts.LargestGroupSize)
	}
}

func TestReduceCrossRefFetchTruncated(t *testing.T) {
	// The window holds fewer issues than the repo's open count.
	facts := ReduceCrossRef([]github.Issue{crossRef(1), crossRef(2)}, 50, 20)
	if !facts.FetchTruncated {
		t.Error("FetchTruncated = false, want true (2 fetched < 50 open)")
	}
}

func TestReduceCrossRefRefsTruncatedFlag(t *testing.T) {
	hub := crossRef(1, 2)
	hub.CrossRefsTruncated = true // more cross-ref events than the fetch cap
	facts := ReduceCrossRef([]github.Issue{hub, crossRef(2)}, 2, 20)
	if !facts.RefsTruncated {
		t.Error("RefsTruncated = false, want true (a member's events were capped)")
	}
}

func TestReduceCrossRefNoReferencesEmptyGroups(t *testing.T) {
	facts := ReduceCrossRef([]github.Issue{crossRef(1), crossRef(2)}, 2, 20)
	if facts.GroupCount != 0 {
		t.Errorf("GroupCount = %d, want 0", facts.GroupCount)
	}
	if facts.Groups == nil {
		t.Error("Groups = nil, want non-nil empty slice (so JSON emits [] not null)")
	}
}

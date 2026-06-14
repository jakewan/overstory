package summary

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// TestReduceMilestonesGroupsMembers pins that fetched open issues are grouped
// under their milestone by number, unmilestoned issues are excluded, and the
// authoritative counts pass through.
func TestReduceMilestonesGroupsMembers(t *testing.T) {
	milestones := []github.Milestone{
		{Number: 7, Title: "Round 5", URL: "m7", OpenIssues: 2, ClosedIssues: 3},
	}
	issues := []github.Issue{
		mkIssue(1, 10, 1, nil, msRef(7, "Round 5")),
		mkIssue(2, 20, 1, nil, msRef(7, "Round 5")),
		mkIssue(3, 5, 1, nil, nil), // unmilestoned — excluded
	}
	facts := ReduceMilestones(milestones, 1, false, issues, 20, now)
	if facts.OpenMilestones != 1 || len(facts.Milestones) != 1 {
		t.Fatalf("OpenMilestones=%d len=%d, want 1/1", facts.OpenMilestones, len(facts.Milestones))
	}
	m := facts.Milestones[0]
	if m.OpenIssues != 2 || m.ClosedIssues != 3 {
		t.Errorf("counts = %d/%d, want 2/3 (authoritative)", m.OpenIssues, m.ClosedIssues)
	}
	if len(m.Members) != 2 {
		t.Fatalf("members = %d, want 2 (issue 3 unmilestoned)", len(m.Members))
	}
	// Oldest-first: issue 2 (age 20) before issue 1 (age 10).
	if m.Members[0].Number != 2 || m.Members[1].Number != 1 {
		t.Errorf("member order = [%d %d], want [2 1] (oldest first)", m.Members[0].Number, m.Members[1].Number)
	}
	if m.MembershipTruncated {
		t.Error("MembershipTruncated = true, want false (2 listed == 2 open)")
	}
}

// TestReduceMilestonesMembershipTruncatedWhenCountExceedsFetched is the M1 case:
// a milestone's authoritative open count exceeds the members present in the
// fetched window, so the membership list is a floor and must say so.
func TestReduceMilestonesMembershipTruncatedWhenCountExceedsFetched(t *testing.T) {
	// Milestone claims 300 open issues, but only 2 were in the fetched window.
	milestones := []github.Milestone{{Number: 7, Title: "Big", OpenIssues: 300}}
	issues := []github.Issue{
		mkIssue(1, 10, 1, nil, msRef(7, "Big")),
		mkIssue(2, 20, 1, nil, msRef(7, "Big")),
	}
	facts := ReduceMilestones(milestones, 1, false, issues, 20, now)
	m := facts.Milestones[0]
	if len(m.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(m.Members))
	}
	if !m.MembershipTruncated {
		t.Error("MembershipTruncated = false, want true (2 listed < 300 open — the list is a floor)")
	}
}

// TestReduceMilestonesMembershipTruncatedWhenListCaps pins that the list cap is
// the other truncation source: more members are present than the limit allows.
func TestReduceMilestonesMembershipTruncatedWhenListCaps(t *testing.T) {
	milestones := []github.Milestone{{Number: 7, Title: "M", OpenIssues: 3}}
	issues := []github.Issue{
		mkIssue(1, 10, 1, nil, msRef(7, "M")),
		mkIssue(2, 20, 1, nil, msRef(7, "M")),
		mkIssue(3, 30, 1, nil, msRef(7, "M")),
	}
	facts := ReduceMilestones(milestones, 1, false, issues, 2, now) // limit 2 < 3 members
	m := facts.Milestones[0]
	if len(m.Members) != 2 || !m.MembershipTruncated {
		t.Errorf("members=%d truncated=%v, want 2/true (capped below open count)", len(m.Members), m.MembershipTruncated)
	}
}

// TestReduceMilestonesExactFetchAndListTruncation pins the milestone-level seams:
// OpenMilestones stays exact and ListTruncated marks a capped milestone list.
func TestReduceMilestonesExactFetchAndListTruncation(t *testing.T) {
	milestones := []github.Milestone{
		{Number: 1, Title: "a", OpenIssues: 0},
		{Number: 2, Title: "b", OpenIssues: 0},
	}
	facts := ReduceMilestones(milestones, 9, true, nil, 1, now) // 9 open total, fetched 2, list cap 1
	if facts.OpenMilestones != 9 {
		t.Errorf("OpenMilestones = %d, want 9 (exact)", facts.OpenMilestones)
	}
	if facts.FetchedCount != 2 || !facts.FetchTruncated {
		t.Errorf("FetchedCount=%d FetchTruncated=%v, want 2/true", facts.FetchedCount, facts.FetchTruncated)
	}
	if len(facts.Milestones) != 1 || !facts.ListTruncated {
		t.Errorf("listed=%d truncated=%v, want 1/true", len(facts.Milestones), facts.ListTruncated)
	}
}

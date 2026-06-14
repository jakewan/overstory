package summary

import (
	"sort"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// MilestoneFacts is the active-milestone progress block: each open milestone's
// authoritative open/closed counts plus the open issues belonging to it from the
// fetched window. Available is false only when the milestone fetch failed; the
// block then degrades rather than failing the whole summary, and Unavailable
// names the reason. OpenMilestones is the repository's exact open-milestone count
// (from the milestone connection), so it stays accurate when the milestone fetch
// truncates (FetchTruncated) or the list caps (ListTruncated).
type MilestoneFacts struct {
	Available      bool                `json:"available"`
	Unavailable    string              `json:"unavailable,omitempty"`
	OpenMilestones int                 `json:"openMilestones"`
	FetchedCount   int                 `json:"fetchedCount"`
	FetchTruncated bool                `json:"fetchTruncated"`
	Milestones     []MilestoneProgress `json:"milestones"`
	Limit          int                 `json:"limit"`
	ListTruncated  bool                `json:"listTruncated"`
}

// MilestoneProgress is one open milestone's progress. OpenIssues and ClosedIssues
// are the milestone's authoritative totals (from the milestone object, not the
// issue window). Members are the open issues from the fetched window that belong
// to this milestone, capped at the list limit. MembershipTruncated is set when
// the member list is a floor — fewer members are listed than the milestone's open
// count — whether because the issue fetch truncated or the list capped, so a
// caller never reads a partial membership as complete.
type MilestoneProgress struct {
	Number              int              `json:"number"`
	Title               string           `json:"title"`
	URL                 string           `json:"url"`
	OpenIssues          int              `json:"openIssues"`
	ClosedIssues        int              `json:"closedIssues"`
	Members             []MilestoneIssue `json:"members"`
	MembershipTruncated bool             `json:"membershipTruncated"`
}

// MilestoneIssue is one open issue belonging to a milestone, reduced to its
// identifying facts and age.
type MilestoneIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	AgeDays int    `json:"ageDays"`
}

// ReduceMilestones reduces the fetched open milestones and open issues to the
// milestone-progress facts as of now. Each milestone's members are the fetched
// open issues associated with it (by milestone number), capped at listLimit and
// ordered oldest-first; MembershipTruncated marks a member list that is a floor
// relative to the milestone's authoritative open count. The milestones list
// itself is capped at listLimit (ListTruncated), ordered by milestone number for
// a stable, total order. totalOpenMilestones keeps OpenMilestones exact when the
// milestone fetch truncates. now is injected so the reduction is deterministic.
func ReduceMilestones(milestones []github.Milestone, totalOpenMilestones int, milestonesFetchTruncated bool, issues []github.Issue, listLimit int, now time.Time) MilestoneFacts {
	facts := MilestoneFacts{
		Available:      true,
		OpenMilestones: totalOpenMilestones,
		FetchedCount:   len(milestones),
		FetchTruncated: milestonesFetchTruncated,
		Limit:          listLimit,
		Milestones:     make([]MilestoneProgress, 0, len(milestones)),
	}

	// Group the fetched open issues by milestone number in one pass; only issues
	// carrying a milestone participate.
	byMilestone := make(map[int][]MilestoneIssue)
	for _, is := range issues {
		if is.Milestone == nil {
			continue
		}
		byMilestone[is.Milestone.Number] = append(byMilestone[is.Milestone.Number], MilestoneIssue{
			Number:  is.Number,
			Title:   is.Title,
			URL:     is.URL,
			AgeDays: reduce.DaysSince(now, is.CreatedAt),
		})
	}

	progress := make([]MilestoneProgress, 0, len(milestones))
	for _, m := range milestones {
		members := byMilestone[m.Number]
		// Oldest-first within the milestone, ties broken by number for a total order.
		sort.Slice(members, func(i, j int) bool {
			if members[i].AgeDays != members[j].AgeDays {
				return members[i].AgeDays > members[j].AgeDays
			}
			return members[i].Number < members[j].Number
		})
		if listLimit >= 0 && len(members) > listLimit {
			members = members[:listLimit]
		}
		progress = append(progress, MilestoneProgress{
			Number:       m.Number,
			Title:        m.Title,
			URL:          m.URL,
			OpenIssues:   m.OpenIssues,
			ClosedIssues: m.ClosedIssues,
			Members:      members,
			// Floor relative to the authoritative open count: capped list or a
			// truncated issue fetch both surface here, never silently.
			MembershipTruncated: len(members) < m.OpenIssues,
		})
	}
	// Milestones by number for a stable order independent of fetch ordering.
	sort.Slice(progress, func(i, j int) bool { return progress[i].Number < progress[j].Number })
	if listLimit >= 0 && len(progress) > listLimit {
		facts.ListTruncated = true
		progress = progress[:listLimit]
	}
	facts.Milestones = progress
	return facts
}

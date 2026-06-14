package summary

import (
	"fmt"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// now is the injected wall clock; all age/inactivity facts are deterministic
// under it.
var now = time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)

// ago returns the instant days before now, for building deterministic
// created/activity timestamps.
func ago(days int) time.Time { return now.AddDate(0, 0, -days) }

// mkIssue builds an open issue with a created age, an inactivity age, labels, and
// an optional milestone reference — the dimensions the orientation reductions
// read. BodyText is set separately by the tests that need it.
func mkIssue(num, ageDays, inactiveDays int, labels []string, ms *github.MilestoneRef) github.Issue {
	return github.Issue{
		Number:         num,
		Title:          fmt.Sprintf("issue %d", num),
		URL:            fmt.Sprintf("u%d", num),
		CreatedAt:      ago(ageDays),
		LastActivityAt: ago(inactiveDays),
		Labels:         labels,
		Milestone:      ms,
	}
}

func msRef(number int, title string) *github.MilestoneRef {
	return &github.MilestoneRef{Number: number, Title: title}
}

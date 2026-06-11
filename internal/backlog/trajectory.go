package backlog

import (
	"sort"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// TrajectoryFacts is the compact result of the creation-vs-closure trajectory
// reduction: for each configured lookback window, how many issues were created,
// how many were closed, and the net — the "is the backlog growing or shrinking"
// signal. It is the one purely aggregate block (no per-issue list), so the per-
// call list limit does not apply to it; its only truncation seam is the fetch.
//
// Available is false only when the underlying activity fetch failed; the block
// then degrades rather than failing the whole review, and Unavailable names the
// reason (e.g. "rate_limited", "fetch_failed") so a caller can tell a throttle
// from a real failure. FetchTruncated is true when the activity fetch hit its cap
// before covering the widest window, making every count a lower bound — never a
// silent truncation.
//
// Windows is the deduplicated, ascending view of the configured windows (so a
// [30, 7, 7, 90] config is reported as [7, 30, 90]). The counts are cumulative
// lookbacks — last-7 issues also count in last-30 and last-90 — not disjoint
// buckets, so a caller must not sum them.
type TrajectoryFacts struct {
	Available      bool               `json:"available"`
	Unavailable    string             `json:"unavailable,omitempty"`
	Windows        []TrajectoryWindow `json:"windows"`
	FetchedCount   int                `json:"fetchedCount"`
	FetchTruncated bool               `json:"fetchTruncated"`
}

// TrajectoryWindow is one lookback window's trajectory: issues Created and Closed
// within the last Days days, and Net (Created − Closed). A negative Net means the
// backlog shrank over the window.
type TrajectoryWindow struct {
	Days    int `json:"days"`
	Created int `json:"created"`
	Closed  int `json:"closed"`
	Net     int `json:"net"`
}

// ReduceTrajectory reduces the fetched issue-activity records to per-window
// creation/closure facts as of now. For each window it counts the issues created
// within the last that-many days and the issues closed within the same span, and
// reports the net. fetchTruncated (from the activity fetch) is passed straight
// through: when set, the windows did not fully cover the widest lookback, so the
// counts are lower bounds.
//
// The window check is an instant comparison (created/closed at or after the
// window's start), not the floored daysSince the staleness block uses — trajectory
// asks "within the last N days" (window membership), a different question from
// staleness's "inactive for at least N days" (a duration threshold). now is
// normalized to UTC so AddDate computes a clean day boundary (the activity
// timestamps are UTC), and is injected so the reduction is deterministic. A
// reopened issue carries no ClosedAt (GitHub clears it on reopen), so it counts as
// open — the "net backlog change as of now" reading.
func ReduceTrajectory(activities []github.IssueActivity, windows []int, fetchTruncated bool, now time.Time) TrajectoryFacts {
	now = now.UTC()
	uniq := dedupeSortedWindows(windows)

	facts := TrajectoryFacts{
		Available:      true,
		FetchedCount:   len(activities),
		FetchTruncated: fetchTruncated,
		Windows:        make([]TrajectoryWindow, 0, len(uniq)),
	}
	for _, days := range uniq {
		start := now.AddDate(0, 0, -days)
		var created, closed int
		for _, a := range activities {
			if !a.CreatedAt.Before(start) {
				created++
			}
			if !a.ClosedAt.IsZero() && !a.ClosedAt.Before(start) {
				closed++
			}
		}
		facts.Windows = append(facts.Windows, TrajectoryWindow{
			Days:    days,
			Created: created,
			Closed:  closed,
			Net:     created - closed,
		})
	}
	return facts
}

// dedupeSortedWindows returns the windows deduplicated and sorted ascending, so
// the reduction's output order is stable and total regardless of how the manifest
// listed them.
func dedupeSortedWindows(windows []int) []int {
	seen := make(map[int]struct{}, len(windows))
	uniq := make([]int, 0, len(windows))
	for _, w := range windows {
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		uniq = append(uniq, w)
	}
	sort.Ints(uniq)
	return uniq
}

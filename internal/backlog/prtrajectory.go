package backlog

import (
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// PRTrajectoryFacts is the compact result of the change-request closure-ratio
// reduction: for each configured lookback window, how many pull requests were
// opened, how many were closed (merged or closed-without-merge), and the net — the
// "is the project keeping pace with incoming PRs, or accumulating an unreviewed PR
// backlog" signal. It is the PR analog of TrajectoryFacts and, like it, is purely
// aggregate (no per-PR list), so the per-call list limit does not apply; its only
// truncation seam is the fetch. It reports counts, not a computed ratio — the
// caller derives whatever ratio it wants to present.
//
// Available is false only when the underlying PR-activity fetch failed; the block
// then degrades rather than failing the whole review, and Unavailable names the
// reason (e.g. "rate_limited", "fetch_failed"). FetchTruncated is true when the
// fetch hit its cap before covering the widest window, making every count a lower
// bound — never a silent truncation.
//
// Windows is the deduplicated, ascending view of the configured windows. The counts
// are cumulative lookbacks — last-7 PRs also count in last-30 and last-90 — not
// disjoint buckets, so a caller must not sum them.
type PRTrajectoryFacts struct {
	Available      bool                 `json:"available"`
	Unavailable    string               `json:"unavailable,omitempty"`
	Windows        []PRTrajectoryWindow `json:"windows"`
	FetchedCount   int                  `json:"fetchedCount"`
	FetchTruncated bool                 `json:"fetchTruncated"`
}

// PRTrajectoryWindow is one lookback window's PR throughput: pull requests Opened
// and Closed within the last Days days, and Net (Opened − Closed). A negative Net
// means more PRs closed than opened over the window — the project gained ground on
// its PR backlog.
type PRTrajectoryWindow struct {
	Days   int `json:"days"`
	Opened int `json:"opened"`
	Closed int `json:"closed"`
	Net    int `json:"net"`
}

// ReducePRTrajectory reduces the fetched pull-request-activity records to per-window
// opened/closed facts as of now. For each window it counts the PRs opened within the
// last that-many days and the PRs closed within the same span, and reports the net.
// fetchTruncated (from the fetch) passes straight through: when set, the windows did
// not fully cover the widest lookback, so the counts are lower bounds.
//
// The window check is an instant comparison (opened/closed at or after the window's
// start) — "within the last N days" (window membership), mirroring ReduceTrajectory.
// now is normalized to UTC and injected so the reduction is deterministic. A reopened
// PR carries no ClosedAt (GitHub clears it on reopen), so it counts as open.
func ReducePRTrajectory(activities []github.PullRequestActivity, windows []int, fetchTruncated bool, now time.Time) PRTrajectoryFacts {
	counts := countTrajectoryWindows(activities, windows, now,
		func(a github.PullRequestActivity) time.Time { return a.CreatedAt },
		func(a github.PullRequestActivity) time.Time { return a.ClosedAt })
	facts := PRTrajectoryFacts{
		Available:      true,
		FetchedCount:   len(activities),
		FetchTruncated: fetchTruncated,
		Windows:        make([]PRTrajectoryWindow, 0, len(counts)),
	}
	for _, c := range counts {
		facts.Windows = append(facts.Windows, PRTrajectoryWindow{
			Days:   c.Days,
			Opened: c.Primary,
			Closed: c.Closed,
			Net:    c.Primary - c.Closed,
		})
	}
	return facts
}

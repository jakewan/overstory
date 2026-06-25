package backlog

import (
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// prTrajNow is the fixed clock the PR-trajectory tests reduce against, so window
// boundaries are deterministic.
var prTrajNow = time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)

// pract builds a PR-activity record relative to prTrajNow. closedDaysAgo < 0 means
// the PR is still open (zero ClosedAt).
func pract(num, openedDaysAgo, closedDaysAgo int) github.PullRequestActivity {
	a := github.PullRequestActivity{Number: num, CreatedAt: prTrajNow.AddDate(0, 0, -openedDaysAgo)}
	if closedDaysAgo >= 0 {
		a.ClosedAt = prTrajNow.AddDate(0, 0, -closedDaysAgo)
	}
	return a
}

func TestReducePRTrajectoryWindowCounts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activities []github.PullRequestActivity
		windows    []int
		want       []PRTrajectoryWindow
	}{
		{
			name:       "opened only",
			activities: []github.PullRequestActivity{pract(1, 3, -1), pract(2, 20, -1)},
			windows:    []int{7, 30},
			want: []PRTrajectoryWindow{
				{Days: 7, Opened: 1, Closed: 0, Net: 1},
				{Days: 30, Opened: 2, Closed: 0, Net: 2},
			},
		},
		{
			name:       "closed only (opened before window)",
			activities: []github.PullRequestActivity{pract(1, 200, 3)},
			windows:    []int{7},
			want:       []PRTrajectoryWindow{{Days: 7, Opened: 0, Closed: 1, Net: -1}},
		},
		{
			name:       "opened and closed in same window count both",
			activities: []github.PullRequestActivity{pract(1, 2, 1)},
			windows:    []int{7},
			want:       []PRTrajectoryWindow{{Days: 7, Opened: 1, Closed: 1, Net: 0}},
		},
		{
			name:       "negative net (more closed than opened — backlog shrinking)",
			activities: []github.PullRequestActivity{pract(1, 2, -1), pract(2, 100, 3), pract(3, 90, 4)},
			windows:    []int{7},
			want:       []PRTrajectoryWindow{{Days: 7, Opened: 1, Closed: 2, Net: -1}},
		},
		{
			name:       "cumulative nesting (last-7 also counts in last-30/-90)",
			activities: []github.PullRequestActivity{pract(1, 3, -1), pract(2, 20, -1), pract(3, 60, -1)},
			windows:    []int{7, 30, 90},
			want: []PRTrajectoryWindow{
				{Days: 7, Opened: 1, Closed: 0, Net: 1},
				{Days: 30, Opened: 2, Closed: 0, Net: 2},
				{Days: 90, Opened: 3, Closed: 0, Net: 3},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReducePRTrajectory(tc.activities, tc.windows, false, prTrajNow)
			if !got.Available {
				t.Fatalf("Available = false, want true")
			}
			if len(got.Windows) != len(tc.want) {
				t.Fatalf("Windows = %+v, want %+v", got.Windows, tc.want)
			}
			for i, w := range tc.want {
				if got.Windows[i] != w {
					t.Errorf("Windows[%d] = %+v, want %+v", i, got.Windows[i], w)
				}
			}
			if got.FetchedCount != len(tc.activities) {
				t.Errorf("FetchedCount = %d, want %d", got.FetchedCount, len(tc.activities))
			}
		})
	}
}

// TestReducePRTrajectoryBoundaryInclusive pins that a PR opened exactly at the
// window start is inside the window (opened/closed at or after the boundary).
func TestReducePRTrajectoryBoundaryInclusive(t *testing.T) {
	got := ReducePRTrajectory([]github.PullRequestActivity{pract(1, 7, -1)}, []int{7}, false, prTrajNow)
	if got.Windows[0].Opened != 1 {
		t.Errorf("Opened = %d, want 1 (opened exactly at the 7-day boundary is in-window)", got.Windows[0].Opened)
	}
}

// TestReducePRTrajectoryDedupsAndSortsWindows pins that the configured windows are
// reported deduplicated and ascending, regardless of input order.
func TestReducePRTrajectoryDedupsAndSortsWindows(t *testing.T) {
	got := ReducePRTrajectory(nil, []int{30, 7, 7, 90}, false, prTrajNow)
	days := make([]int, len(got.Windows))
	for i, w := range got.Windows {
		days[i] = w.Days
	}
	if len(days) != 3 || days[0] != 7 || days[1] != 30 || days[2] != 90 {
		t.Errorf("window days = %v, want [7 30 90] (deduped, ascending)", days)
	}
}

// TestReducePRTrajectoryEmptyWindows pins that no configured windows yields a
// non-nil empty list (serializes as [] not null), still Available.
func TestReducePRTrajectoryEmptyWindows(t *testing.T) {
	got := ReducePRTrajectory([]github.PullRequestActivity{pract(1, 2, -1)}, nil, false, prTrajNow)
	if !got.Available {
		t.Error("Available = false, want true")
	}
	if got.Windows == nil {
		t.Error("Windows = nil, want non-nil empty slice")
	}
	if len(got.Windows) != 0 {
		t.Errorf("Windows = %+v, want empty", got.Windows)
	}
}

// TestReducePRTrajectoryFetchTruncatedPassthrough pins that the fetch's truncation
// flag threads straight to the facts, so a caller knows the counts are a floor.
func TestReducePRTrajectoryFetchTruncatedPassthrough(t *testing.T) {
	got := ReducePRTrajectory([]github.PullRequestActivity{pract(1, 2, -1)}, []int{7}, true, prTrajNow)
	if !got.FetchTruncated {
		t.Error("FetchTruncated = false, want true (passed through from the fetch)")
	}
}

package backlog

import (
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// trajNow is the fixed clock the trajectory tests reduce against, so window
// boundaries are deterministic.
var trajNow = time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)

// act builds an activity record relative to trajNow. closedDaysAgo < 0 means the
// issue is still open (zero ClosedAt).
func act(num, createdDaysAgo, closedDaysAgo int) github.IssueActivity {
	a := github.IssueActivity{Number: num, CreatedAt: trajNow.AddDate(0, 0, -createdDaysAgo)}
	if closedDaysAgo >= 0 {
		a.ClosedAt = trajNow.AddDate(0, 0, -closedDaysAgo)
	}
	return a
}

func TestReduceTrajectoryWindowCounts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activities []github.IssueActivity
		windows    []int
		want       []TrajectoryWindow
	}{
		{
			name:       "created only",
			activities: []github.IssueActivity{act(1, 3, -1), act(2, 20, -1)},
			windows:    []int{7, 30},
			want: []TrajectoryWindow{
				{Days: 7, Created: 1, Closed: 0, Net: 1},
				{Days: 30, Created: 2, Closed: 0, Net: 2},
			},
		},
		{
			name:       "closed only (created before window)",
			activities: []github.IssueActivity{act(1, 200, 3)},
			windows:    []int{7},
			want:       []TrajectoryWindow{{Days: 7, Created: 0, Closed: 1, Net: -1}},
		},
		{
			name:       "created and closed in same window count both",
			activities: []github.IssueActivity{act(1, 2, 1)},
			windows:    []int{7},
			want:       []TrajectoryWindow{{Days: 7, Created: 1, Closed: 1, Net: 0}},
		},
		{
			name:       "negative net (more closed than created)",
			activities: []github.IssueActivity{act(1, 2, -1), act(2, 100, 3), act(3, 90, 4)},
			windows:    []int{7},
			want:       []TrajectoryWindow{{Days: 7, Created: 1, Closed: 2, Net: -1}},
		},
		{
			name:       "cumulative nesting (last-7 also counts in last-30/-90)",
			activities: []github.IssueActivity{act(1, 3, -1), act(2, 20, -1), act(3, 60, -1)},
			windows:    []int{7, 30, 90},
			want: []TrajectoryWindow{
				{Days: 7, Created: 1, Closed: 0, Net: 1},
				{Days: 30, Created: 2, Closed: 0, Net: 2},
				{Days: 90, Created: 3, Closed: 0, Net: 3},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReduceTrajectory(tc.activities, tc.windows, false, trajNow)
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

// TestReduceTrajectoryBoundaryInclusive pins that an issue created exactly at the
// window start is inside the window (created/closed at or after the boundary).
func TestReduceTrajectoryBoundaryInclusive(t *testing.T) {
	got := ReduceTrajectory([]github.IssueActivity{act(1, 7, -1)}, []int{7}, false, trajNow)
	if got.Windows[0].Created != 1 {
		t.Errorf("Created = %d, want 1 (created exactly at the 7-day boundary is in-window)", got.Windows[0].Created)
	}
}

// TestReduceTrajectoryDedupsAndSortsWindows pins that the configured windows are
// reported deduplicated and ascending, regardless of input order.
func TestReduceTrajectoryDedupsAndSortsWindows(t *testing.T) {
	got := ReduceTrajectory(nil, []int{30, 7, 7, 90}, false, trajNow)
	days := make([]int, len(got.Windows))
	for i, w := range got.Windows {
		days[i] = w.Days
	}
	if len(days) != 3 || days[0] != 7 || days[1] != 30 || days[2] != 90 {
		t.Errorf("window days = %v, want [7 30 90] (deduped, ascending)", days)
	}
}

// TestReduceTrajectoryEmptyWindows pins that no configured windows yields a non-nil
// empty list (serializes as [] not null), still Available.
func TestReduceTrajectoryEmptyWindows(t *testing.T) {
	got := ReduceTrajectory([]github.IssueActivity{act(1, 2, -1)}, nil, false, trajNow)
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

// TestReduceTrajectoryFetchTruncatedPassthrough pins that the fetch's truncation
// flag threads straight to the facts, so a caller knows the counts are a floor.
func TestReduceTrajectoryFetchTruncatedPassthrough(t *testing.T) {
	got := ReduceTrajectory([]github.IssueActivity{act(1, 2, -1)}, []int{7}, true, trajNow)
	if !got.FetchTruncated {
		t.Error("FetchTruncated = false, want true (passed through from the fetch)")
	}
}

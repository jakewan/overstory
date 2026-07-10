package reduce

import (
	"testing"
	"time"
)

// TestDaysSince pins the two behavioral edges DaysSince carries — the future-
// timestamp clamp and the whole-day floor — which consumers otherwise exercise
// only incidentally, so a regression to either would surface as a confusing
// downstream reduction bug rather than a local failure.
func TestDaysSince(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		then time.Time
		want int
	}{
		{"same instant is zero", now, 0},
		{"future timestamp clamps to zero", now.Add(48 * time.Hour), 0},
		{"just under a day floors to zero", now.Add(-(24*time.Hour - time.Minute)), 0},
		{"exactly one day", now.Add(-24 * time.Hour), 1},
		{"just under two days floors to one", now.Add(-(48*time.Hour - time.Minute)), 1},
		{"several whole days", now.Add(-10 * 24 * time.Hour), 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DaysSince(now, tc.then); got != tc.want {
				t.Errorf("DaysSince(now, now%s) = %d, want %d", tc.then.Sub(now), got, tc.want)
			}
		})
	}
}

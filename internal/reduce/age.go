package reduce

import "time"

// DaysSince returns whole days between then and now, floored, clamped at 0 so
// clock skew (a future timestamp) cannot produce a negative count.
func DaysSince(now, then time.Time) int {
	d := now.Sub(then)
	if d < 0 {
		return 0
	}
	return int(d.Hours() / 24)
}

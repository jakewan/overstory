// Package backlog holds overstory's issue-reduction logic: pure functions that
// turn a repository's fetched issues into compact structured facts. It depends
// on no MCP or transport types — only on the issue shape it reduces — so the
// reductions are deterministic and trivially testable.
package backlog

import (
	"sort"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// StalenessFacts is the compact result of a staleness reduction. It carries
// facts and counts only — no prose or rendering. OpenIssueCount is exact (the
// repository's true open-issue total); the bucket and stale-issue figures are
// computed over the fetched window, which FetchTruncated marks as a floor when
// the backlog exceeds what was fetched.
type StalenessFacts struct {
	Repo            string            `json:"repo"`
	ThresholdDays   int               `json:"thresholdDays"`
	ThresholdSource string            `json:"thresholdSource"`
	GeneratedAt     time.Time         `json:"generatedAt"`
	OpenIssueCount  int               `json:"openIssueCount"`
	FetchedCount    int               `json:"fetchedCount"`
	StaleCount      int               `json:"staleCount"`
	FreshCount      int               `json:"freshCount"`
	Buckets         []StalenessBucket `json:"buckets"`
	StaleIssues     []StaleIssue      `json:"staleIssues"`
	Limit           int               `json:"limit"`
	ListTruncated   bool              `json:"listTruncated"`
	FetchLimit      int               `json:"fetchLimit"`
	FetchTruncated  bool              `json:"fetchTruncated"`
}

// StalenessBucket counts stale issues in an inactivity band, in days. MaxDays 0
// marks the open-ended top band. The band is emitted as facts; the caller
// formats any human label.
type StalenessBucket struct {
	MinDays int `json:"minDays"`
	MaxDays int `json:"maxDays"`
	Count   int `json:"count"`
}

// StaleIssue is one stale issue reduced to its identifying facts. InactiveDays
// is measured from the last human activity; AgeDays from creation.
type StaleIssue struct {
	Number              int       `json:"number"`
	Title               string    `json:"title"`
	URL                 string    `json:"url"`
	InactiveDays        int       `json:"inactiveDays"`
	AgeDays             int       `json:"ageDays"`
	LastHumanActivityAt time.Time `json:"lastHumanActivityAt"`
}

// ReduceStaleness reduces the fetched open issues to staleness facts as of now.
// An issue is stale when its inactivity (days since last human activity) is at
// least thresholdDays. totalOpen is the repository's exact open count, so
// OpenIssueCount stays accurate even when fewer issues were fetched; the listed
// stale issues are capped at listLimit (most-stale first). now is injected so
// the reduction is deterministic.
func ReduceStaleness(issues []github.Issue, totalOpen, thresholdDays, listLimit int, now time.Time) StalenessFacts {
	facts := StalenessFacts{
		ThresholdDays:  thresholdDays,
		GeneratedAt:    now,
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		Limit:          listLimit,
		FetchTruncated: len(issues) < totalOpen,
	}

	stale := make([]StaleIssue, 0, len(issues))
	for _, is := range issues {
		inactive := daysSince(now, is.LastActivityAt)
		if inactive < thresholdDays {
			continue
		}
		stale = append(stale, StaleIssue{
			Number:              is.Number,
			Title:               is.Title,
			URL:                 is.URL,
			InactiveDays:        inactive,
			AgeDays:             daysSince(now, is.CreatedAt),
			LastHumanActivityAt: is.LastActivityAt,
		})
	}

	facts.StaleCount = len(stale)
	facts.FreshCount = len(issues) - len(stale)
	facts.Buckets = bucketize(stale, thresholdDays)

	// Most-stale first; ties broken by issue number for deterministic output.
	sort.Slice(stale, func(i, j int) bool {
		if stale[i].InactiveDays != stale[j].InactiveDays {
			return stale[i].InactiveDays > stale[j].InactiveDays
		}
		return stale[i].Number < stale[j].Number
	})

	if listLimit >= 0 && len(stale) > listLimit {
		facts.ListTruncated = true
		stale = stale[:listLimit]
	}
	facts.StaleIssues = stale
	return facts
}

// daysSince returns whole days between then and now, floored, clamped at 0 so
// clock skew (a future timestamp) cannot produce a negative count.
func daysSince(now, then time.Time) int {
	d := now.Sub(then)
	if d < 0 {
		return 0
	}
	return int(d.Hours() / 24)
}

// bucketize counts the stale issues into threshold-relative inactivity bands —
// [t,2t), [2t,3t), [3t,∞) — so the bands stay meaningful for any repo's
// threshold. The three bands are always present (count 0 when empty) for a
// stable output shape.
func bucketize(stale []StaleIssue, threshold int) []StalenessBucket {
	buckets := []StalenessBucket{
		{MinDays: threshold, MaxDays: 2 * threshold},
		{MinDays: 2 * threshold, MaxDays: 3 * threshold},
		{MinDays: 3 * threshold, MaxDays: 0},
	}
	for _, s := range stale {
		switch {
		case s.InactiveDays < 2*threshold:
			buckets[0].Count++
		case s.InactiveDays < 3*threshold:
			buckets[1].Count++
		default:
			buckets[2].Count++
		}
	}
	return buckets
}

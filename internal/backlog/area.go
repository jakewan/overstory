package backlog

import (
	"sort"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// AreaBalanceFacts is the compact result of the area-balance reduction: how the
// fetched open issues distribute across a repo's functional areas. Areas are
// identified by labels (explicit list and/or configurable prefix rules);
// per-area counts overlap (a multi-area issue counts in each), so they need not
// sum to the issue total. Unclassified — issues matching no area — is first-class
// and is often the largest bucket. OpenIssueCount stays exact even when the fetch
// window is truncated, which FetchTruncated marks.
type AreaBalanceFacts struct {
	OpenIssueCount int         `json:"openIssueCount"`
	FetchedCount   int         `json:"fetchedCount"`
	Areas          []AreaCount `json:"areas"`
	Unclassified   int         `json:"unclassified"`
	MultiAreaCount int         `json:"multiAreaCount"`
	FetchTruncated bool        `json:"fetchTruncated"`
}

// AreaCount is one area and its open-issue count. Area is the canonical display
// name; buckets are keyed internally by the normalized name, so casing and
// match-source variants of one area collapse into a single bucket.
type AreaCount struct {
	Area  string `json:"area"`
	Count int    `json:"count"`
}

// ReduceAreaBalance reduces the fetched open issues to an area distribution. An
// issue's labels are mapped through the matcher (explicit list + prefix rules);
// each matched name is normalized to a canonical key so an explicit form ("Core")
// and a prefix suffix ("core") collapse into one area. The displayed name is the
// lexicographically-smallest original form seen for the key (deterministic, and
// in the common case there is only one form per key). totalOpen keeps
// OpenIssueCount exact when the window is truncated.
func ReduceAreaBalance(issues []github.Issue, totalOpen int, labels []string, prefixes []reduce.PrefixRule) AreaBalanceFacts {
	facts := AreaBalanceFacts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Areas:          make([]AreaCount, 0),
	}
	matcher := reduce.NewLabelMatcher(labels, prefixes)

	type bucket struct {
		display string
		count   int
	}
	areas := make(map[string]*bucket)

	for _, is := range issues {
		// Distinct area keys on this issue, so a multi-label issue counts once per
		// area and MultiAreaCount reflects distinct areas, not labels.
		keys := make(map[string]struct{})
		for _, label := range is.Labels {
			name, ok := matcher.Match(label)
			if !ok {
				continue
			}
			key := reduce.NormalizeLabel(name)
			keys[key] = struct{}{}
			b, exists := areas[key]
			if !exists {
				areas[key] = &bucket{display: name}
			} else if name < b.display {
				// Track the canonical display across all occurrences; min is
				// order-independent, so accumulation stays deterministic.
				b.display = name
			}
		}
		if len(keys) == 0 {
			facts.Unclassified++
			continue
		}
		if len(keys) > 1 {
			facts.MultiAreaCount++
		}
		for key := range keys {
			areas[key].count++
		}
	}

	for _, b := range areas {
		facts.Areas = append(facts.Areas, AreaCount{Area: b.display, Count: b.count})
	}
	// Hot spots first; tie-break by display name (unique per key) for a total order.
	sort.Slice(facts.Areas, func(i, j int) bool {
		if facts.Areas[i].Count != facts.Areas[j].Count {
			return facts.Areas[i].Count > facts.Areas[j].Count
		}
		return facts.Areas[i].Area < facts.Areas[j].Area
	})
	return facts
}

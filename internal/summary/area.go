package summary

import (
	"sort"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// AreaInventoryFacts is the per-area active/deferred inventory: for each
// functional area (manifest-declared labels and prefixes), how many of its open
// issues are active versus deferred, so a caller sees where the live work and the
// parked work sit. Per-area counts overlap a multi-area issue, so they need not
// sum to OpenIssueCount. Unclassified collects issues matching no area, split the
// same way. OpenIssueCount stays exact when the fetch window truncates
// (FetchTruncated).
type AreaInventoryFacts struct {
	OpenIssueCount int         `json:"openIssueCount"`
	FetchedCount   int         `json:"fetchedCount"`
	FetchTruncated bool        `json:"fetchTruncated"`
	Areas          []AreaCount `json:"areas"`
	Unclassified   AreaCount   `json:"unclassified"`
}

// AreaCount is one area's split between Active (open, not deferred) and Deferred
// (carrying a deferred label) open issues. Area is the canonical display name;
// the Unclassified bucket leaves it empty.
type AreaCount struct {
	Area     string `json:"area"`
	Active   int    `json:"active"`
	Deferred int    `json:"deferred"`
}

// ReduceAreaInventory reduces the fetched open issues to a per-area active/
// deferred split. An issue is "deferred" when it carries a configured deferred
// label, "active" otherwise; it counts once per distinct area it matches (so a
// multi-area issue contributes to each), and into Unclassified when it matches no
// area. The area taxonomy and deferred labels come from the same manifest
// conventions the backlog reductions consume. totalOpen keeps OpenIssueCount
// exact when the window is truncated.
func ReduceAreaInventory(issues []github.Issue, totalOpen int, areaLabels []string, areaPrefixes []reduce.PrefixRule, deferredLabels []string) AreaInventoryFacts {
	facts := AreaInventoryFacts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Areas:          make([]AreaCount, 0),
	}
	areaMatcher := reduce.NewLabelMatcher(areaLabels, areaPrefixes)
	deferredMatcher := reduce.NewLabelMatcher(deferredLabels, nil)

	type bucket struct {
		display  string
		active   int
		deferred int
	}
	areas := make(map[string]*bucket)

	for _, is := range issues {
		deferred := deferredMatcher.MatchesAny(is.Labels)

		// Distinct area keys on this issue, so a multi-label issue counts once per
		// area rather than once per matching label.
		keys := make(map[string]struct{})
		for _, label := range is.Labels {
			name, ok := areaMatcher.Match(label)
			if !ok {
				continue
			}
			key := reduce.NormalizeLabel(name)
			keys[key] = struct{}{}
			b, exists := areas[key]
			if !exists {
				areas[key] = &bucket{display: name}
			} else if name < b.display {
				// Canonical display is the smallest original form across *all*
				// occurrences (min is order-independent, so accumulation stays
				// deterministic) — the same rule backlog.ReduceAreaBalance applies,
				// so the two tools never disagree on one area's name.
				b.display = name
			}
		}
		if len(keys) == 0 {
			if deferred {
				facts.Unclassified.Deferred++
			} else {
				facts.Unclassified.Active++
			}
			continue
		}
		for key := range keys {
			b := areas[key]
			if deferred {
				b.deferred++
			} else {
				b.active++
			}
		}
	}

	for _, b := range areas {
		facts.Areas = append(facts.Areas, AreaCount{Area: b.display, Active: b.active, Deferred: b.deferred})
	}
	// Busiest areas first (active+deferred), tie-break by name for a total order.
	sort.Slice(facts.Areas, func(i, j int) bool {
		ti := facts.Areas[i].Active + facts.Areas[i].Deferred
		tj := facts.Areas[j].Active + facts.Areas[j].Deferred
		if ti != tj {
			return ti > tj
		}
		return facts.Areas[i].Area < facts.Areas[j].Area
	})
	return facts
}

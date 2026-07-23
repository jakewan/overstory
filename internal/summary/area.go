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
// area. Classification and canonical naming come from reduce.AreaClassifier,
// shared with the grooming read so both tools name an area identically; the
// deferred labels come from the same manifest conventions. totalOpen keeps
// OpenIssueCount exact when the window is truncated.
func ReduceAreaInventory(issues []github.Issue, totalOpen int, areaLabels []string, areaPrefixes []reduce.PrefixRule, deferredLabels []string) AreaInventoryFacts {
	facts := AreaInventoryFacts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Areas:          make([]AreaCount, 0),
	}
	classifier := reduce.NewAreaClassifier(areaLabels, areaPrefixes)
	deferredMatcher := reduce.NewLabelMatcher(deferredLabels, nil)

	type split struct{ active, deferred int }
	areas := make(map[string]*split)

	for _, is := range issues {
		deferred := deferredMatcher.MatchesAny(is.Labels)

		// Keys is distinct per issue, so a multi-label issue counts once per area
		// rather than once per matching label.
		keys := classifier.Keys(is.Labels)
		if len(keys) == 0 {
			if deferred {
				facts.Unclassified.Deferred++
			} else {
				facts.Unclassified.Active++
			}
			continue
		}
		for key := range keys {
			s, ok := areas[key]
			if !ok {
				s = &split{}
				areas[key] = s
			}
			if deferred {
				s.deferred++
			} else {
				s.active++
			}
		}
	}

	for key, s := range areas {
		facts.Areas = append(facts.Areas, AreaCount{
			Area: classifier.Display(key), Active: s.active, Deferred: s.deferred,
		})
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

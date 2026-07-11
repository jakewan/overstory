package backlog

import (
	"sort"
	"strings"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// QualityParams is the resolved quality convention a reduction runs against, the
// backlog-layer mirror of the manifest's QualityConfig (kept distinct so this
// package stays decoupled from convention resolution). MinBodyLength is the
// minimum trimmed body length before a body reads as substantive; Categories are
// the required label families (empty = the per-category check is not configured).
type QualityParams struct {
	MinBodyLength int
	Categories    []Category
}

// Category is one required label family: an issue satisfies it by carrying a
// label that matches by explicit Labels and/or Prefixes. Name is the display
// name echoed in the per-category counts.
type Category struct {
	Name     string
	Labels   []string
	Prefixes []reduce.PrefixRule
}

// QualityFacts is the compact result of the quality reduction: open issues that
// fail one or more grooming checks (a too-thin body, no labels at all, or — when
// configured — a missing required-label category), reduced to per-check counts
// plus a capped, granular list a caller renders per project.
//
// Per-check counts overlap and need not sum to FlaggedCount: a zero-label issue
// is counted in NoLabelsCount and in every MissingCategoryCounts entry, and the
// same issue contributes to FlaggedCount once. MinBodyLength is echoed so a
// caller knows the bar applied (1 = must be non-empty, 0 = body check disabled).
// OpenIssueCount stays exact even when the fetch window is truncated, which
// FetchTruncated marks.
type QualityFacts struct {
	MinBodyLength         int            `json:"minBodyLength"`
	OpenIssueCount        int            `json:"openIssueCount"`
	FetchedCount          int            `json:"fetchedCount"`
	MissingBodyCount      int            `json:"missingBodyCount"`
	NoLabelsCount         int            `json:"noLabelsCount"`
	CategoriesConfigured  bool           `json:"categoriesConfigured"`
	ConfiguredCategories  []string       `json:"configuredCategories"`
	MissingCategoryCounts map[string]int `json:"missingCategoryCounts"`
	FlaggedCount          int            `json:"flaggedCount"`
	FlaggedIssues         []QualityIssue `json:"flaggedIssues"`
	Limit                 int            `json:"limit"`
	ListTruncated         bool           `json:"listTruncated"`
	FetchTruncated        bool           `json:"fetchTruncated"`
}

// QualityIssue is one flagged open issue reduced to its identifying facts plus the
// granular per-check results, so a caller can render exactly the signal that
// matters for its project. BodyLength is always the trimmed body-text length —
// reported even when the body check is disabled — so thinness stays visible.
type QualityIssue struct {
	Number            int      `json:"number"`
	Title             string   `json:"title"`
	URL               string   `json:"url"`
	AgeDays           int      `json:"ageDays"`
	BodyLength        int      `json:"bodyLength"`
	MissingBody       bool     `json:"missingBody"`
	LabelCount        int      `json:"labelCount"`
	NoLabels          bool     `json:"noLabels"`
	MissingCategories []string `json:"missingCategories"`
}

// ReduceQuality reduces the fetched open issues to quality facts as of now. An
// issue is flagged when it fails at least one active check: a trimmed body length
// below params.MinBodyLength (so MinBodyLength 1 flags empty bodies, a higher
// value flags thin ones, and 0 disables the check since a length is never < 0),
// no labels at all, or — when categories are configured — missing a label in a
// required family (matched case-insensitively, reusing the shared matcher).
// totalOpen keeps OpenIssueCount exact when the window is truncated; the listed
// issues are capped at listLimit, most-incomplete first. now is injected so the
// reduction is deterministic.
func ReduceQuality(issues []github.Issue, totalOpen int, params QualityParams, listLimit int, now time.Time) QualityFacts {
	facts := QualityFacts{
		MinBodyLength:         params.MinBodyLength,
		OpenIssueCount:        totalOpen,
		FetchedCount:          len(issues),
		Limit:                 listLimit,
		FetchTruncated:        len(issues) < totalOpen,
		CategoriesConfigured:  len(params.Categories) > 0,
		ConfiguredCategories:  make([]string, 0, len(params.Categories)),
		MissingCategoryCounts: make(map[string]int, len(params.Categories)),
		FlaggedIssues:         make([]QualityIssue, 0),
	}

	// One matcher per category, in declared order; the count keys are pre-seeded to
	// 0 so a configured category with no misses still appears in the output.
	type catMatcher struct {
		name    string
		matcher reduce.LabelMatcher
	}
	cats := make([]catMatcher, 0, len(params.Categories))
	for _, c := range params.Categories {
		cats = append(cats, catMatcher{name: c.Name, matcher: reduce.NewLabelMatcher(c.Labels, c.Prefixes)})
		facts.ConfiguredCategories = append(facts.ConfiguredCategories, c.Name)
		facts.MissingCategoryCounts[c.Name] = 0
	}

	flagged := make([]QualityIssue, 0, len(issues))
	for _, is := range issues {
		bodyLen := len(strings.TrimSpace(is.BodyText))
		missingBody := bodyLen < params.MinBodyLength
		labelCount := len(is.Labels)
		noLabels := labelCount == 0

		missingCats := make([]string, 0, len(cats))
		for _, cm := range cats {
			if !cm.matcher.MatchesAny(is.Labels) {
				missingCats = append(missingCats, cm.name)
			}
		}
		sort.Strings(missingCats) // deterministic regardless of declaration order

		if !missingBody && !noLabels && len(missingCats) == 0 {
			continue
		}

		// Aggregates are untruncated (the full count), unlike the capped list.
		if missingBody {
			facts.MissingBodyCount++
		}
		if noLabels {
			facts.NoLabelsCount++
		}
		for _, name := range missingCats {
			facts.MissingCategoryCounts[name]++
		}

		flagged = append(flagged, QualityIssue{
			Number:            is.Number,
			Title:             is.Title,
			URL:               is.URL,
			AgeDays:           reduce.DaysSince(now, is.CreatedAt),
			BodyLength:        bodyLen,
			MissingBody:       missingBody,
			LabelCount:        labelCount,
			NoLabels:          noLabels,
			MissingCategories: missingCats,
		})
	}
	facts.FlaggedCount = len(flagged)

	// Most-incomplete first (more failed checks = higher grooming priority); ties
	// broken by issue number for a total order.
	sort.Slice(flagged, func(i, j int) bool {
		fi, fj := failedChecks(flagged[i]), failedChecks(flagged[j])
		if fi != fj {
			return fi > fj
		}
		return flagged[i].Number < flagged[j].Number
	})

	if listLimit >= 0 && len(flagged) > listLimit {
		facts.ListTruncated = true
		flagged = flagged[:listLimit]
	}
	facts.FlaggedIssues = flagged
	return facts
}

// failedChecks counts the distinct grooming checks an issue failed, ranking how
// incomplete it is for the most-incomplete-first ordering.
func failedChecks(q QualityIssue) int {
	n := len(q.MissingCategories)
	if q.MissingBody {
		n++
	}
	if q.NoLabels {
		n++
	}
	return n
}

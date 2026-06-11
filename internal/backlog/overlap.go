package backlog

import (
	"sort"
	"strings"
	"unicode"

	"github.com/jakewan/overstory/internal/github"
)

// OverlapParams is the resolved title-overlap convention a reduction runs
// against, the backlog-layer mirror of the manifest's OverlapConfig (kept
// distinct so this package stays decoupled from convention resolution).
// TitleThreshold is the char-trigram Sørensen–Dice score two titles must reach
// to be linked (0 disables the reduction; 1 means exact-match only).
type OverlapParams struct {
	TitleThreshold float64
}

// OverlapFacts is the compact result of the title-overlap reduction: groups of
// open issues whose titles are similar enough to be candidate duplicates, found
// by linking issue pairs whose char-trigram Sørensen–Dice score clears the
// threshold and taking the connected components of size >= 2.
//
// Truncation here is unlike the count-based blocks (staleness, quality). Overlap
// is a *pairwise relation computed only over the fetched window*, so
// FetchTruncated means a true duplicate sitting outside the window is
// undetectable — not merely unlisted. A caller must not read a non-truncated run
// as "every duplicate in the repo"; it is "every duplicate among the fetched
// issues". LargestGroupSize is surfaced so a caller can spot a runaway transitive
// cluster (titles chained A~B~C without A~C) that an empty SharedTokens list
// would otherwise hide.
type OverlapFacts struct {
	TitleThreshold   float64        `json:"titleThreshold"`
	OpenIssueCount   int            `json:"openIssueCount"`
	FetchedCount     int            `json:"fetchedCount"`
	GroupCount       int            `json:"groupCount"`
	OverlappingCount int            `json:"overlappingCount"`
	LargestGroupSize int            `json:"largestGroupSize"`
	Groups           []OverlapGroup `json:"groups"`
	Limit            int            `json:"limit"`
	ListTruncated    bool           `json:"listTruncated"`
	FetchTruncated   bool           `json:"fetchTruncated"`
}

// OverlapGroup is one cluster of similar-titled open issues. SharedTokens are the
// normalized words appearing in at least two member titles — human-readable
// evidence for *why* the issues grouped, deliberately word-based even though the
// linking metric is char-trigram, so a caller can render the overlap. It can be
// empty for a transitively-chained group whose members share no single word.
type OverlapGroup struct {
	Issues       []OverlapIssue `json:"issues"`
	SharedTokens []string       `json:"sharedTokens"`
}

// OverlapIssue is one member of an overlap group, reduced to its identifying
// facts.
type OverlapIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// ReduceOverlap reduces the fetched open issues to title-overlap facts: it links
// every pair of issues whose normalized titles reach params.TitleThreshold under
// char-trigram Sørensen–Dice, then reports the connected components of size >= 2
// as groups. A threshold of 0 disables the reduction (no groups). totalOpen keeps
// OpenIssueCount exact when the window is truncated; the listed groups are capped
// at listLimit (the cap applies to groups, not member issues), largest first.
// The reduction is time-independent, so it takes no clock.
func ReduceOverlap(issues []github.Issue, totalOpen int, params OverlapParams, listLimit int) OverlapFacts {
	facts := OverlapFacts{
		TitleThreshold: params.TitleThreshold,
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		Limit:          listLimit,
		FetchTruncated: len(issues) < totalOpen,
		Groups:         make([]OverlapGroup, 0),
	}
	// A 0 threshold disables the reduction; return before the per-issue shingling so
	// the disabled path costs nothing proportional to the fetched window.
	if params.TitleThreshold <= 0 {
		return facts
	}

	// Precompute each title once: its trigram multiset (the linking metric) and its
	// word set (the rendered evidence). This keeps edge detection O(n^2) in
	// comparisons but only O(n) in shingling, which matters because the fetch limit
	// — and thus n — is operator-configured and unbounded in manifest validation.
	docs := make([]titleDoc, len(issues))
	for i, is := range issues {
		norm := normalizeTitle(is.Title)
		docs[i] = titleDoc{issue: is, norm: norm, grams: trigrams(norm), words: wordSet(norm)}
	}

	edges := titleEdges(docs, params.TitleThreshold)
	components := connectedComponents(len(docs), edges)

	for _, comp := range components {
		facts.OverlappingCount += len(comp)
		facts.LargestGroupSize = max(facts.LargestGroupSize, len(comp))
		facts.Groups = append(facts.Groups, buildGroup(docs, comp))
	}
	facts.GroupCount = len(facts.Groups)

	// Largest groups first (the strongest duplicate-cluster signal); ties broken by
	// the smallest member number for a total, deterministic order.
	sort.Slice(facts.Groups, func(i, j int) bool {
		gi, gj := facts.Groups[i], facts.Groups[j]
		if len(gi.Issues) != len(gj.Issues) {
			return len(gi.Issues) > len(gj.Issues)
		}
		return gi.Issues[0].Number < gj.Issues[0].Number
	})

	if listLimit >= 0 && len(facts.Groups) > listLimit {
		facts.ListTruncated = true
		facts.Groups = facts.Groups[:listLimit]
	}
	return facts
}

// titleDoc is the precomputed form of one issue's title: the normalized string,
// its trigram multiset (linking metric), and its word set (rendered evidence).
type titleDoc struct {
	issue github.Issue
	norm  string
	grams map[string]int
	words map[string]struct{}
}

// titleEdges links every issue pair whose titles are similar enough. A threshold
// of 0 disables linking entirely (returns no edges) — the disable sentinel.
// Two guards correct the raw metric at its extremes: a title that normalizes to
// empty (emoji-only, punctuation-only) never links, so unrelated issues aren't
// clustered by the Sørensen–Dice empty-vs-empty special case; and byte-identical
// titles shorter than a trigram (e.g. "CI") link via an exact-match short-circuit
// the trigram score would otherwise miss with a 0.0.
func titleEdges(docs []titleDoc, threshold float64) [][2]int {
	if threshold <= 0 {
		return nil
	}
	var edges [][2]int
	for i := 0; i < len(docs); i++ {
		if docs[i].norm == "" {
			continue
		}
		for j := i + 1; j < len(docs); j++ {
			if docs[j].norm == "" {
				continue
			}
			if docs[i].norm == docs[j].norm || dice(docs[i].grams, docs[j].grams) >= threshold {
				edges = append(edges, [2]int{i, j})
			}
		}
	}
	return edges
}

// buildGroup assembles an OverlapGroup from a component's doc indices: its member
// issues sorted by number, and the words shared by >= 2 members as evidence.
func buildGroup(docs []titleDoc, comp []int) OverlapGroup {
	g := OverlapGroup{Issues: make([]OverlapIssue, 0, len(comp))}
	wordCounts := make(map[string]int)
	for _, idx := range comp {
		d := docs[idx]
		g.Issues = append(g.Issues, OverlapIssue{Number: d.issue.Number, Title: d.issue.Title, URL: d.issue.URL})
		for w := range d.words {
			wordCounts[w]++
		}
	}
	sort.Slice(g.Issues, func(i, j int) bool { return g.Issues[i].Number < g.Issues[j].Number })

	shared := make([]string, 0)
	for w, c := range wordCounts {
		if c >= 2 {
			shared = append(shared, w)
		}
	}
	sort.Strings(shared) // deterministic regardless of map iteration order
	g.SharedTokens = shared
	return g
}

// normalizeTitle lowercases a title and reduces it to space-separated
// alphanumeric runs: every non-letter, non-digit rune becomes a separator. It
// works on runes so multibyte content (including CJK, which counts as letters and
// survives) isn't mis-shingled. A title with no alphanumeric content normalizes
// to empty, which titleEdges treats as un-linkable.
func normalizeTitle(s string) string {
	var b strings.Builder
	prevSpace := true // leading run is "after a space", so no leading separator emitted
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// wordSet is the set of whitespace-separated tokens in an already-normalized
// title, used for shared-word evidence (distinct from the trigram metric).
func wordSet(norm string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, w := range strings.Fields(norm) {
		set[w] = struct{}{}
	}
	return set
}

// trigrams is the char-trigram multiset of a normalized title: a map from each
// 3-rune window to its occurrence count. A title shorter than 3 runes has no
// trigrams (an empty map), which dice scores as 0 against anything.
func trigrams(s string) map[string]int {
	rs := []rune(s)
	m := make(map[string]int, max(0, len(rs)-2))
	for i := 0; i+3 <= len(rs); i++ {
		m[string(rs[i:i+3])]++
	}
	return m
}

// dice is the Sørensen–Dice coefficient over two trigram multisets:
// 2*|intersection| / (|a|+|b|), where sizes are summed counts (multiset
// cardinality). An empty multiset on either side scores 0 — so an empty-titled
// issue never links, overriding the metric's degenerate empty-vs-empty = 1.
func dice(a, b map[string]int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	sizeA, sizeB, inter := 0, 0, 0
	for _, c := range a {
		sizeA += c
	}
	for gram, cb := range b {
		sizeB += cb
		if ca, ok := a[gram]; ok {
			inter += min(ca, cb)
		}
	}
	return 2 * float64(inter) / float64(sizeA+sizeB)
}

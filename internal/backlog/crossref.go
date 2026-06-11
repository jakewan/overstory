package backlog

import (
	"sort"

	"github.com/jakewan/overstory/internal/github"
)

// CrossRefFacts is the compact result of the cross-reference reduction: groups of
// open issues that reference one another via GitHub's cross-reference graph
// (issue-to-issue, same repository), found by taking the connected components of
// size >= 2 over those reference edges. It is the candidate-consolidation signal —
// a topic scattered across several open issues — distinct from title similarity.
//
// Truncation here has three independent seams. FetchTruncated means the window
// itself was capped, so a referencing issue outside it is invisible — an edge to it
// is undetectable, not merely unlisted, and a caller must not read a non-truncated
// run as "every cross-reference in the repo"; it is "every cross-reference among
// the fetched issues". ListTruncated means more groups existed than Limit allowed.
// RefsTruncated means at least one fetched issue had more cross-reference events
// than the per-issue fetch cap, so a graph edge may be missing within the window
// (conservative: flagged even when the dropped events might all be pull-request
// sources, since after the cap they can't be told apart). LargestGroupSize is
// surfaced so a caller can spot a runaway hub cluster the capped Groups list hides.
type CrossRefFacts struct {
	OpenIssueCount   int             `json:"openIssueCount"`
	FetchedCount     int             `json:"fetchedCount"`
	GroupCount       int             `json:"groupCount"`
	LinkedCount      int             `json:"linkedCount"`
	LargestGroupSize int             `json:"largestGroupSize"`
	Groups           []CrossRefGroup `json:"groups"`
	Limit            int             `json:"limit"`
	ListTruncated    bool            `json:"listTruncated"`
	FetchTruncated   bool            `json:"fetchTruncated"`
	RefsTruncated    bool            `json:"refsTruncated"`
}

// CrossRefGroup is one cluster of cross-referencing open issues. References are the
// directed edges among the members — human-readable evidence for why the issues
// grouped, the cross-reference analog of OverlapGroup's SharedTokens — each running
// From the issue that authored the reference To the issue it referenced.
type CrossRefGroup struct {
	Issues     []CrossRefIssue `json:"issues"`
	References []CrossRef      `json:"references"`
}

// CrossRefIssue is one member of a cross-reference group, reduced to its
// identifying facts.
type CrossRefIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// CrossRef is one directed cross-reference edge: From references To.
type CrossRef struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// ReduceCrossRef reduces the fetched open issues to cross-reference facts: it links
// every pair of issues where one cross-references the other (per each issue's
// ReferencedBy projection, restricted to pairs both in the fetched window), then
// reports the connected components of size >= 2 as groups. totalOpen keeps
// OpenIssueCount exact when the window is truncated; the listed groups are capped at
// listLimit (the cap applies to groups, not members), largest first. The reduction
// is time-independent, so it takes no clock.
func ReduceCrossRef(issues []github.Issue, totalOpen int, listLimit int) CrossRefFacts {
	facts := CrossRefFacts{
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		Limit:          listLimit,
		FetchTruncated: len(issues) < totalOpen,
		Groups:         make([]CrossRefGroup, 0),
	}

	// Map issue number -> position, so a reference to an issue outside the fetched
	// window (closed, or truncated out) forms no edge.
	pos := make(map[int]int, len(issues))
	for i, is := range issues {
		pos[is.Number] = i
	}

	var edges [][2]int
	// directed[i] holds the reference edges whose target is issues[i], so a
	// component's evidence can be gathered from its members without recomputing
	// direction.
	directed := make([][]CrossRef, len(issues))
	for i, is := range issues {
		if is.CrossRefsTruncated {
			facts.RefsTruncated = true
		}
		for _, ref := range is.ReferencedBy {
			if ref == is.Number { // self-reference (by number, never slice index): no edge
				continue
			}
			j, ok := pos[ref]
			if !ok { // referencer outside the fetched window: no node to link
				continue
			}
			edges = append(edges, [2]int{j, i})
			directed[i] = append(directed[i], CrossRef{From: ref, To: is.Number})
		}
	}

	for _, comp := range connectedComponents(len(issues), edges) {
		facts.LinkedCount += len(comp)
		facts.LargestGroupSize = max(facts.LargestGroupSize, len(comp))
		facts.Groups = append(facts.Groups, buildCrossRefGroup(issues, directed, comp))
	}
	facts.GroupCount = len(facts.Groups)

	// Largest groups first (the strongest consolidation signal); ties broken by the
	// smallest member number for a total, deterministic order.
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

// buildCrossRefGroup assembles a CrossRefGroup from a component's indices: its
// member issues sorted by number, and the directed reference edges among them
// (each edge recorded once, on its target) sorted by (From, To) for determinism.
func buildCrossRefGroup(issues []github.Issue, directed [][]CrossRef, comp []int) CrossRefGroup {
	g := CrossRefGroup{
		Issues:     make([]CrossRefIssue, 0, len(comp)),
		References: make([]CrossRef, 0),
	}
	for _, idx := range comp {
		is := issues[idx]
		g.Issues = append(g.Issues, CrossRefIssue{Number: is.Number, Title: is.Title, URL: is.URL})
		g.References = append(g.References, directed[idx]...)
	}
	sort.Slice(g.Issues, func(i, j int) bool { return g.Issues[i].Number < g.Issues[j].Number })
	sort.Slice(g.References, func(i, j int) bool {
		if g.References[i].From != g.References[j].From {
			return g.References[i].From < g.References[j].From
		}
		return g.References[i].To < g.References[j].To
	})
	return g
}

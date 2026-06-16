// Package criticalpath holds overstory's critical-path / gate reduction: a pure
// function that groups a repository's open critical-path-labeled issues into the
// operator-declared, ordered list of streams and reports, per stream, a
// gate-cleared signal. It is shared by both the orientation (project_summary) and
// grooming (backlog_review) reductions — the block is identical in both, only the
// rendering differs — so it lives in its own package rather than in either
// reduction, importing only the fetched shapes (github) and the shared label
// primitives (reduce); neither reduction depends on the other.
package criticalpath

import (
	"sort"
	"strings"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// Facts is the compact result of the critical-path reduction. Configured is true
// exactly when the repo declared a stream list and a label; when false the
// reduction is a no-op (empty Streams, zero counts) rather than an error, because
// a critical path is repo-specific and has no generic default — the same posture
// the deferred reduction takes. Configured intentionally does NOT reuse the
// Available/Unavailable degrade idiom the fetch-backed blocks use: this reduction
// runs over an already-fetched corpus and has no fetch of its own to fail.
//
// OpenIssueCount is the repository-wide open-issue total (not the critical-path
// subset); it stays exact even when the fetch window truncates, which
// FetchTruncated marks. A critical-path-labeled issue that matches no declared
// stream is surfaced, never dropped, split by cause: UnareaedCount (no area label
// at all — a triage gap) versus OffPathCount (a real area that is not a declared
// stream — usually a misconfiguration, or a critical path missing an area that
// carries blocking work).
type Facts struct {
	Configured     bool     `json:"configured"`
	OpenIssueCount int      `json:"openIssueCount"`
	FetchedCount   int      `json:"fetchedCount"`
	FetchTruncated bool     `json:"fetchTruncated"`
	Streams        []Stream `json:"streams"`
	UnareaedCount  int      `json:"unareaedCount"`
	OffPathCount   int      `json:"offPathCount"`
}

// Stream is one declared critical-path stream and the open critical-path issues in
// it. GateCleared is true when no open critical-path issue remains in the stream —
// the signal a caller uses to decide a downstream stream may begin. Two caveats the
// caller must respect, both consequences of reducing over open issues alone:
//
//   - It is a windowed fact. When the enclosing Facts.FetchTruncated is true, a
//     cleared gate is provisional — an unfetched critical-path issue may remain — so
//     a caller treats GateCleared as authoritative only when FetchTruncated is false.
//   - It witnesses absence, not completion. "Every critical-path issue closed" and
//     "no critical-path issue ever existed" both present as no members and a cleared
//     gate; the gate reports the absence of open blockers, not that work was done.
//
// Members are the open critical-path issues, ascending by issue number (a
// deterministic total order), then capped at the caller's list limit, with
// ListTruncated marking the cap. GateCleared is computed from the full matched set
// before that cap, so it is correct regardless of the limit.
type Stream struct {
	Stream        string   `json:"stream"`
	GateCleared   bool     `json:"gateCleared"`
	Members       []Member `json:"members"`
	ListTruncated bool     `json:"listTruncated"`
}

// Member is one open critical-path issue reduced to its identifying facts.
type Member struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// Params are the resolved conventions the reduction needs: the ordered declared
// Streams and the critical-path Label, plus the area taxonomy (AreaLabels,
// AreaPrefixes) reused from the area-balance convention — streams are areas, so an
// issue's stream is its area match against the same matcher the area reductions
// use.
type Params struct {
	Streams      []string
	Label        string
	AreaLabels   []string
	AreaPrefixes []reduce.PrefixRule
}

// Reduce groups the fetched open issues into the declared streams. An issue is
// considered only if it carries the critical-path label. It is classified per
// issue with precedence so a multi-area issue resolves to exactly one disposition:
// if it matches any declared stream it is a member of each such stream (and is not
// counted off-path); else if it matched a real area that is not a declared stream
// it is off-path; else (no area match) it is unareaed. Streams are emitted in
// declared order, every declared stream present even when it has no member.
// totalOpen keeps OpenIssueCount exact when the window is truncated.
func Reduce(issues []github.Issue, totalOpen int, params Params, listLimit int) Facts {
	facts := Facts{
		Configured:     len(params.Streams) > 0,
		OpenIssueCount: totalOpen,
		FetchedCount:   len(issues),
		FetchTruncated: len(issues) < totalOpen,
		Streams:        make([]Stream, 0, len(params.Streams)),
	}
	if !facts.Configured {
		return facts
	}

	areaMatcher := reduce.NewLabelMatcher(params.AreaLabels, params.AreaPrefixes)
	cpMatcher := reduce.NewLabelMatcher([]string{params.Label}, nil)

	type bucket struct {
		display string
		members []Member
	}
	order := make([]string, 0, len(params.Streams))
	buckets := make(map[string]*bucket, len(params.Streams))
	for _, s := range params.Streams {
		key := reduce.NormalizeLabel(s)
		// Validation rejects duplicate streams; skip defensively so a stray dup
		// never produces two buckets for one canonical name.
		if _, ok := buckets[key]; ok {
			continue
		}
		order = append(order, key)
		buckets[key] = &bucket{display: strings.TrimSpace(s)}
	}

	for _, is := range issues {
		if !anyMatch(cpMatcher, is.Labels) {
			continue
		}
		// Distinct area keys for this issue, so a multi-label issue is counted once
		// per area rather than once per matching label.
		areaKeys := make(map[string]struct{})
		for _, label := range is.Labels {
			name, ok := areaMatcher.Match(label)
			if !ok {
				continue
			}
			areaKeys[reduce.NormalizeLabel(name)] = struct{}{}
		}
		// Precedence: membership in any declared stream wins over the off-path /
		// unareaed buckets, so an on-path issue that also touches a non-stream area
		// is never double-counted.
		matchedStream := false
		for key := range areaKeys {
			if b, ok := buckets[key]; ok {
				b.members = append(b.members, Member{Number: is.Number, Title: is.Title})
				matchedStream = true
			}
		}
		if matchedStream {
			continue
		}
		if len(areaKeys) > 0 {
			facts.OffPathCount++
		} else {
			facts.UnareaedCount++
		}
	}

	for _, key := range order {
		b := buckets[key]
		members := b.members
		// Gate reads the full matched set before the list cap, so it is correct for
		// any listLimit (including 0, which a direct caller may pass).
		gateCleared := len(members) == 0
		sort.Slice(members, func(i, j int) bool { return members[i].Number < members[j].Number })
		listTruncated := false
		if listLimit >= 0 && len(members) > listLimit {
			listTruncated = true
			members = members[:listLimit]
		}
		if members == nil {
			members = make([]Member, 0)
		}
		facts.Streams = append(facts.Streams, Stream{
			Stream:        b.display,
			GateCleared:   gateCleared,
			Members:       members,
			ListTruncated: listTruncated,
		})
	}
	return facts
}

// anyMatch reports whether any of the labels matches the matcher.
func anyMatch(m reduce.LabelMatcher, labels []string) bool {
	for _, l := range labels {
		if _, ok := m.Match(l); ok {
			return true
		}
	}
	return false
}

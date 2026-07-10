// Package criticalpath holds overstory's critical-path / gate reduction: a pure
// function that groups a repository's open critical-path-labeled issues into the
// operator-declared, ordered list of streams and reports, per stream, a
// gate-cleared signal. It produces one block, identical for the orientation
// (project_summary) and grooming (backlog_review) reads — only the rendering
// differs — so rather than living in either reduction package it sits on its own,
// shared by both without either depending on the other. It imports only the
// fetched shapes (github) and the shared label primitives (reduce).
package criticalpath

import (
	"sort"
	"strings"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// Facts is the compact result of the critical-path reduction. Configured is true
// exactly when the repo declared a stream list and a label; when false the
// reduction is a no-op (empty Streams) rather than an error, because a critical
// path is repo-specific and has no generic default — the same posture the deferred
// reduction takes.
//
// Available distinguishes a successful reduction (including the not-configured
// no-op) from a degraded one: when the block is sourced from a dedicated
// label-scoped fetch (the truncated-window path in the server) and that fetch
// fails, the handler marks the block Available:false with Unavailable naming the
// reason, degrading rather than failing the whole call — the same idiom the
// milestone and pull-request blocks use. Reduce itself always yields
// Available:true; the degraded value is constructed by the handler without calling
// Reduce.
//
// FetchedCount is the number of critical-path-labeled issues the reduction saw (the
// matched subset, not the whole source window). FetchTruncated marks an incomplete
// source set — the labeled fetch itself truncated — so a cleared gate over it is
// provisional.
//
// LabelTruncatedCount is the second provisional axis, orthogonal to FetchTruncated:
// the number of on-path members whose own label list was capped (github.Issue.
// LabelsTruncated). A member's stream is its area label, so a member whose area label
// fell in the truncated tail is misassigned — out of its real stream into Unareaed/
// OffPath or another stream — silently emptying its stream and clearing that gate. So
// when LabelTruncatedCount > 0 a cleared gate is provisional: a hidden member could
// belong to any stream. It is a lower bound on the general-window path, where an issue
// whose critical-path label itself was truncated out never matched and so was never
// counted here; on the labeled-fetch path the filter label is guaranteed present, so
// only area assignment (not membership) is at risk.
//
// A critical-path-labeled issue that matches no declared stream is
// surfaced, never dropped, split by cause: UnareaedCount (no area label at all — a
// triage gap) versus OffPathCount (a real area that is not a declared stream —
// usually a misconfiguration, or a critical path missing an area that carries
// blocking work).
type Facts struct {
	Configured          bool     `json:"configured"`
	Available           bool     `json:"available"`
	Unavailable         string   `json:"unavailable,omitempty"`
	FetchedCount        int      `json:"fetchedCount"`
	FetchTruncated      bool     `json:"fetchTruncated"`
	LabelTruncatedCount int      `json:"labelTruncatedCount"`
	Streams             []Stream `json:"streams"`
	UnareaedCount       int      `json:"unareaedCount"`
	OffPathCount        int      `json:"offPathCount"`
}

// Stream is one declared critical-path stream and the open critical-path issues in
// it. GateCleared is true when no open critical-path issue remains in the stream —
// the signal a caller uses to decide a downstream stream may begin. Two caveats the
// caller must respect:
//
//   - Authoritative on a complete source set with untruncated member labels,
//     provisional otherwise. The block is sourced from the critical-path-labeled
//     subset the gate actually depends on, so a cleared gate is authoritative when
//     both Facts.FetchTruncated and Facts.LabelTruncatedCount are zero — the common
//     case, since that labeled subset is small and bounded and issues rarely exceed
//     the per-issue label cap. FetchTruncated marks the rare tail where even the
//     labeled fetch truncated (more labeled issues than the fetch cap); the separate
//     LabelTruncatedCount marks an on-path member whose area label was capped and so
//     may have been misassigned out of this stream. Either leaves a cleared gate
//     provisional.
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

// Configured reports whether the params declare both a stream list and a
// non-blank label — the condition under which the reduction can classify issues.
// Guarded here (not only at the manifest layer) so a direct caller passing streams
// with an empty label reports not-configured rather than configured-but-matching-
// nothing (which would clear every gate). Reduce sets Facts.Configured from it, and
// the server's fetch gate reads it to decide whether to fetch at all, so it is the
// single definition both share rather than a predicate recomputed at each site.
func (p Params) Configured() bool {
	return len(p.Streams) > 0 && strings.TrimSpace(p.Label) != ""
}

// Reduce groups the fetched open issues into the declared streams. An issue is
// considered only if it carries the critical-path label. It is classified per
// issue with precedence so a multi-area issue resolves to exactly one disposition:
// if it matches any declared stream it is a member of each such stream (and is not
// counted off-path); else if it matched a real area that is not a declared stream
// it is off-path; else (no area match) it is unareaed. Streams are emitted in
// declared order, every declared stream present even when it has no member.
//
// complete says whether the source set covers every open critical-path issue; the
// handler sets it per source path (true when the general open-issue window was not
// truncated, or when a dedicated labeled fetch was not truncated). It drives
// FetchTruncated, so a cleared gate over an incomplete set reads provisional. The
// critical-path label filter runs here regardless of source, so Reduce is valid
// over the general window (path 1) or an already-labeled fetch (path 2) alike.
func Reduce(issues []github.Issue, complete bool, params Params, listLimit int) Facts {
	facts := Facts{
		Configured: params.Configured(),
		// A completed reduction, always — the degraded Available:false value is the
		// handler's, built without calling Reduce.
		Available:      true,
		FetchTruncated: !complete,
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
		// Count every critical-path issue reduced over — the matched subset, coherent
		// whether the source is the general window (path 1) or a labeled fetch (path 2).
		facts.FetchedCount++
		// A member whose own labels were capped may have had its area (stream) label
		// truncated out, so its stream assignment — and any gate it would clear — is
		// provisional. Counted on the on-path set only; see the Facts godoc for the
		// path-1 lower-bound caveat.
		if is.LabelsTruncated {
			facts.LabelTruncatedCount++
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

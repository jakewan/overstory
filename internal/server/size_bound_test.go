package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/backlog"
	"github.com/jakewan/overstory/internal/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bulkyDeferredIssues builds n stale, deferred issues with enough per-item detail
// (labels, body refs, native edges) that the composite response is large enough
// to breach a small size budget — the large-repo condition #74 is about.
func bulkyDeferredIssues(n int) []github.Issue {
	issues := make([]github.Issue, 0, n)
	for i := 1; i <= n; i++ {
		is := deferredIssue(i, daysAgo(120), "deferred", "area/simulation", "needs-triage")
		is.Title = fmt.Sprintf("a reasonably descriptive issue title number %d that adds bytes", i)
		is.BodyText = fmt.Sprintf("this issue body is long enough to contribute meaningful bytes to the payload for issue %d", i)
		is.BlockedBy = []github.DependencyRef{{Number: i + 4000, Open: true}, {Number: i + 5000, Open: true}}
		issues = append(issues, is)
	}
	return issues
}

// structuredLen is the actual marshaled byte length of the structured result the
// client receives — the real wire measurement, not the self-reported FinalBytes.
func structuredLen(t *testing.T, res *mcp.CallToolResult) int {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	return len(raw)
}

// TestBacklogReviewBoundsResponseSize is the BDD driver for #74's reliability
// fix: on a backlog large enough to breach the configured budget, the response
// comes back bounded (marker present, measured size within budget, counts intact)
// instead of oversized.
func TestBacklogReviewBoundsResponseSize(t *testing.T) {
	const maxBytes = 6000
	root := writeManifestDir(t, fmt.Sprintf(
		"acme/widgets:\n  deferred:\n    labels: [deferred]\n  response:\n    maxBytes: %d\n", maxBytes))
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    bulkyDeferredIssues(80),
		TotalOpen: 80,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 100})
	facts := decodeFacts(t, res)

	if facts.SizeBound == nil {
		t.Fatalf("SizeBound = nil, want a marker on an over-budget response")
	}
	// The budget sits above the irreducible floor for this fixture, so the bound is
	// achievable and the real marshaled size must be within it.
	if got := structuredLen(t, res); got > maxBytes {
		t.Errorf("structured size = %d bytes, want <= %d", got, maxBytes)
	}
	// Counts survive trimming: the deferred block still reports all 80, even though
	// its item list was cut.
	if facts.Deferred.DeferredCount != 80 {
		t.Errorf("Deferred.DeferredCount = %d, want 80 (counts are never trimmed)", facts.Deferred.DeferredCount)
	}
	if !facts.Deferred.ListTruncated || len(facts.Deferred.DeferredIssues) >= 80 {
		t.Errorf("deferred list not trimmed: listTruncated=%v len=%d", facts.Deferred.ListTruncated, len(facts.Deferred.DeferredIssues))
	}
	// The marker attributes the trim.
	var sawDeferred bool
	for _, tb := range facts.SizeBound.TrimmedBlocks {
		if tb.Block == "deferred" {
			sawDeferred = true
			if tb.Dropped <= 0 || tb.Remaining < 0 {
				t.Errorf("deferred TrimmedBlock = %+v, want positive dropped", tb)
			}
		}
	}
	if !sawDeferred {
		t.Errorf("TrimmedBlocks = %+v, want a deferred entry", facts.SizeBound.TrimmedBlocks)
	}
}

// TestBacklogReviewWitnessesWireDuplication documents the SDK behavior the size
// budget calibrates for: the facts cross the wire twice — once in
// StructuredContent, once in a back-compat TextContent block.
func TestBacklogReviewWitnessesWireDuplication(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  deferred:\n    labels: [deferred]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{deferredIssue(1, daysAgo(120), "deferred")},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})

	text := contentText(res)
	if strings.TrimSpace(text) == "" {
		t.Fatalf("no TextContent block; expected the SDK back-compat copy of the facts")
	}
	var fromText backlog.Facts
	if err := json.Unmarshal([]byte(text), &fromText); err != nil {
		t.Fatalf("TextContent is not the facts JSON: %v", err)
	}
	if fromText.Repo != "acme/widgets" {
		t.Errorf("TextContent facts Repo = %q, want the same facts as StructuredContent", fromText.Repo)
	}
}

// TestBacklogReviewNoBoundOnSmallResponse pins the normal-path invariant: a
// response under budget carries no marker (key omitted), so existing consumers
// see byte-identical output.
func TestBacklogReviewNoBoundOnSmallResponse(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  deferred:\n    labels: [deferred]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{deferredIssue(1, daysAgo(120), "deferred")},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.SizeBound != nil {
		t.Errorf("SizeBound = %+v, want nil on an under-budget response", facts.SizeBound)
	}
}

// TestProjectSummaryBoundsResponseSize mirrors the reliability fix on the
// orientation read.
func TestProjectSummaryBoundsResponseSize(t *testing.T) {
	const maxBytes = 6000
	root := writeManifestDir(t, fmt.Sprintf(
		"acme/widgets:\n  response:\n    maxBytes: %d\n", maxBytes))
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    bulkyDeferredIssues(80),
		TotalOpen: 80,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 100})
	facts := decodeSummary(t, res)

	if facts.SizeBound == nil {
		t.Fatalf("SizeBound = nil, want a marker on an over-budget response")
	}
	if got := structuredLen(t, res); got > maxBytes {
		t.Errorf("structured size = %d bytes, want <= %d", got, maxBytes)
	}
	if facts.Recommendations.OpenIssueCount != 80 {
		t.Errorf("Recommendations.OpenIssueCount = %d, want 80 (counts never trimmed)", facts.Recommendations.OpenIssueCount)
	}
}

// bulkyMilestoneTracks builds milestones whose descriptions parse into tracksPer
// bold-run-in tracks each carrying membersPer inline issue refs — enough member
// tuples to breach a small budget. The refs are kept ref-dense (bare `#N`, minimal
// prose) so the irreducible description floor stays below budget and member trimming
// can actually bring the response under it; a prose-dense description would leave the
// floor over budget (the honest partial-bound residual, pinned separately below).
func bulkyMilestoneTracks(milestones, tracksPer, membersPer int) []github.Milestone {
	ms := make([]github.Milestone, 0, milestones)
	ref := 0
	for m := 1; m <= milestones; m++ {
		var b strings.Builder
		fmt.Fprintf(&b, "## Milestone %d tracks\n\n", m)
		for tk := 1; tk <= tracksPer; tk++ {
			fmt.Fprintf(&b, "**Track %d**:\n", tk)
			for range membersPer {
				ref++
				fmt.Fprintf(&b, "#%d ", ref)
			}
			b.WriteString("\n\n")
		}
		ms = append(ms, github.Milestone{
			Number:      m,
			Title:       fmt.Sprintf("milestone %d", m),
			URL:         "u",
			OpenIssues:  tracksPer * membersPer,
			Description: b.String(),
		})
	}
	return ms
}

// TestMilestoneTracksBoundKeepsHeadlinesTrimsMembers is the BDD driver for #84: on a
// milestone set large enough to breach the budget, the response comes back bounded —
// every milestone and track headline preserved, the per-track member lists trimmed to
// fit, and the marker attributing the trim. The leaf (members) carries the bytes; the
// summary above it is never dropped.
func TestMilestoneTracksBoundKeepsHeadlinesTrimsMembers(t *testing.T) {
	// The budget sits above the irreducible floor (descriptions + headlines + the
	// marker's own per-track entries) so the member trim can reach it — the ref-dense
	// fixture keeps that floor small. A prose-dense fixture would push the floor over
	// budget, which is the honest partial-bound residual pinned separately below.
	const maxBytes = 8000
	const milestones, tracksPer, membersPer = 6, 2, 60
	root := writeManifestDir(t, fmt.Sprintf("acme/widgets:\n  response:\n    maxBytes: %d\n", maxBytes))
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: bulkyMilestoneTracks(milestones, tracksPer, membersPer),
		TotalOpen:  milestones,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 100})
	facts := decodeMilestoneTracks(t, res)

	if facts.SizeBound == nil {
		t.Fatalf("SizeBound = nil, want a marker on an over-budget response")
	}
	if got := structuredLen(t, res); got > maxBytes {
		t.Errorf("structured size = %d bytes, want <= %d", got, maxBytes)
	}
	// Every milestone and track headline survives — members-only trimming never drops
	// a headline.
	if len(facts.Milestones) != milestones {
		t.Errorf("milestone entries = %d, want all %d to survive", len(facts.Milestones), milestones)
	}
	for _, m := range facts.Milestones {
		if len(m.Tracks) != tracksPer {
			t.Errorf("milestone #%d tracks = %d, want all %d to survive", m.Number, len(m.Tracks), tracksPer)
		}
	}
	// Members carried the bytes, so at least one track's list was trimmed with its flag.
	listed, sawTrimmed := 0, false
	for _, m := range facts.Milestones {
		for _, tr := range m.Tracks {
			listed += len(tr.Members)
			if tr.ListTruncated {
				sawTrimmed = true
			}
		}
	}
	if listed >= milestones*tracksPer*membersPer {
		t.Errorf("listed members = %d, want < %d (members trimmed to fit)", listed, milestones*tracksPer*membersPer)
	}
	if !sawTrimmed {
		t.Errorf("no track's ListTruncated set, want the trimmed lists flagged")
	}
	// The marker attributes the trim to a per-track member block.
	var sawMemberBlock bool
	for _, tb := range facts.SizeBound.TrimmedBlocks {
		if strings.HasPrefix(tb.Block, "milestones[#") && strings.HasSuffix(tb.Block, ".members") {
			sawMemberBlock = true
			if tb.Dropped <= 0 || tb.Remaining < 0 {
				t.Errorf("member TrimmedBlock = %+v, want positive dropped", tb)
			}
		}
	}
	if !sawMemberBlock {
		t.Errorf("TrimmedBlocks = %+v, want a milestones[#N].tracks[M].members entry", facts.SizeBound.TrimmedBlocks)
	}
}

// TestMilestoneTracksNoBoundOnSmallResponse pins the normal-path invariant: a small
// response carries no marker (key omitted), so existing consumers see byte-identical
// output.
func TestMilestoneTracksNoBoundOnSmallResponse(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{
			{Number: 1, Title: "M1", URL: "u", OpenIssues: 2, Description: "**Track A**:\n#1 #2\n"},
		},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.SizeBound != nil {
		t.Errorf("SizeBound = %+v, want nil on an under-budget response", facts.SizeBound)
	}
}

// TestMilestoneTracksProseDescriptionBoundIsBestEffort pins the honest degraded case
// of the settled members-only decision (#84): a verbatim milestone description is
// "unbounded by design" and members-only trimming cannot shed it, so when the
// description floor alone exceeds the budget the bound is best-effort — the marker is
// stamped and FinalBytes honestly reports the overflow rather than falsely claiming
// success. This is a real partial-bound residual the composites do not have.
func TestMilestoneTracksProseDescriptionBoundIsBestEffort(t *testing.T) {
	const maxBytes = 4096 // the manifest floor; the prose below exceeds it on its own
	root := writeManifestDir(t, fmt.Sprintf("acme/widgets:\n  response:\n    maxBytes: %d\n", maxBytes))
	prose := strings.Repeat("This milestone description is operator-authored planning prose. ", 100)
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{{Number: 1, Title: "big", URL: "u", OpenIssues: 1, Description: prose}},
		TotalOpen:  1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.SizeBound == nil {
		t.Fatalf("SizeBound = nil, want a best-effort marker when the description floor exceeds the budget")
	}
	if facts.SizeBound.FinalBytes <= maxBytes {
		t.Errorf("FinalBytes = %d, want > %d (honest overflow on the irreducible description floor)", facts.SizeBound.FinalBytes, maxBytes)
	}
	// The milestone headline still survives — there was nothing trimmable to drop.
	if len(facts.Milestones) != 1 {
		t.Errorf("milestones = %d, want 1 (headline preserved)", len(facts.Milestones))
	}
}

// TestMilestoneTracksFloorOverflowWithUnitsShortCircuits is the real-units witness of
// the floor-overflow path (#101): unlike the prose test above (zero parsed tracks, so
// drop-all does nothing), this fixture is ref-dense — its descriptions parse into many
// track/member trim units — yet the verbatim descriptions themselves already exceed the
// budget. The bound must empty every member list (the short-circuit's drop-all),
// attribute the drops in the marker, and honestly report the residual overflow via
// FinalBytes, all while preserving every headline. This exercises the real
// trimUnit→boundResponse wiring on the many-units drain the fix targets, which the
// prose fixture cannot.
func TestMilestoneTracksFloorOverflowWithUnitsShortCircuits(t *testing.T) {
	// Ref-dense descriptions whose sheer bulk (untrimmable verbatim text) exceeds the
	// budget on their own, even after every member list is emptied.
	const maxBytes = 4096
	const milestones, tracksPer, membersPer = 8, 3, 50
	root := writeManifestDir(t, fmt.Sprintf("acme/widgets:\n  response:\n    maxBytes: %d\n", maxBytes))
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: bulkyMilestoneTracks(milestones, tracksPer, membersPer),
		TotalOpen:  milestones,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 100})
	facts := decodeMilestoneTracks(t, res)

	if facts.SizeBound == nil {
		t.Fatalf("SizeBound = nil, want a best-effort marker when the description floor exceeds the budget")
	}
	if facts.SizeBound.FinalBytes <= maxBytes {
		t.Fatalf("FinalBytes = %d, want > %d — this fixture's description floor must exceed the budget so the short-circuit fires", facts.SizeBound.FinalBytes, maxBytes)
	}
	// Every headline survives; every member list is emptied by the drop-all probe.
	if len(facts.Milestones) != milestones {
		t.Errorf("milestone entries = %d, want all %d preserved", len(facts.Milestones), milestones)
	}
	for _, m := range facts.Milestones {
		if len(m.Tracks) != tracksPer {
			t.Errorf("milestone #%d tracks = %d, want all %d preserved", m.Number, len(m.Tracks), tracksPer)
		}
		for _, tr := range m.Tracks {
			if len(tr.Members) != 0 {
				t.Errorf("milestone #%d track %q members = %d, want 0 (all trimmed on the floor overflow)", m.Number, tr.Label, len(tr.Members))
			}
		}
	}
	// The marker attributes the drop-all to per-track member blocks with the full member
	// count dropped and none remaining — the same tally the incremental drain would reach.
	memberBlocks := 0
	for _, tb := range facts.SizeBound.TrimmedBlocks {
		if strings.HasPrefix(tb.Block, "milestones[#") && strings.HasSuffix(tb.Block, ".members") {
			memberBlocks++
			if tb.Dropped != membersPer || tb.Remaining != 0 {
				t.Errorf("%s: dropped=%d remaining=%d, want %d/0", tb.Block, tb.Dropped, tb.Remaining, membersPer)
			}
		}
	}
	if memberBlocks != milestones*tracksPer {
		t.Errorf("member TrimmedBlocks = %d, want %d (one per track)", memberBlocks, milestones*tracksPer)
	}
}

// TestMilestoneTracksDegradedFetchNoBound locks the seam the byte bound introduced on
// the degrade path: a failed milestone fetch yields an empty (non-nil) Milestones
// slice, so the bound registers zero trim units, measures a tiny payload, and leaves
// no marker — a degraded block never spuriously reports a size bound.
func TestMilestoneTracksDegradedFetchNoBound(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	fetcher := fakeFetcher{milestonesErr: github.ErrRepoNotFound}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.Available {
		t.Errorf("Available = true, want false on fetch failure")
	}
	if facts.SizeBound != nil {
		t.Errorf("SizeBound = %+v, want nil on a degraded fetch", facts.SizeBound)
	}
}

// TestProjectSummaryBoundKeepsMilestoneEntriesTrimsMembers pins the milestone trim
// shape: the bound sheds milestone *members* (the bytes) while every milestone's
// headline progress entry survives — never a whole-milestone drop, which (since
// progress sorts by number ascending) would shed the newest/active milestone first.
func TestProjectSummaryBoundKeepsMilestoneEntriesTrimsMembers(t *testing.T) {
	const maxBytes = 6000
	const milestones, perMilestone = 8, 50
	root := writeManifestDir(t, fmt.Sprintf("acme/widgets:\n  response:\n    maxBytes: %d\n", maxBytes))

	var issues []github.Issue
	var ms []github.Milestone
	num := 0
	for m := 1; m <= milestones; m++ {
		title := fmt.Sprintf("milestone %d", m)
		ms = append(ms, github.Milestone{Number: m, Title: title, URL: "u", OpenIssues: perMilestone})
		for range perMilestone {
			num++
			issues = append(issues, summaryIssue(num, &github.MilestoneRef{Number: m, Title: title}))
		}
	}
	fetcher := fakeFetcher{
		result:     github.IssueListResult{Issues: issues, TotalOpen: len(issues)},
		milestones: github.MilestoneListResult{Milestones: ms, TotalOpen: milestones},
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 100}))

	if facts.SizeBound == nil {
		t.Fatalf("SizeBound = nil, want a bounded response")
	}
	// Every milestone entry survives — the headline orientation signal is preserved.
	if len(facts.Milestones.Milestones) != milestones {
		t.Errorf("milestone entries = %d, want all %d to survive the bound", len(facts.Milestones.Milestones), milestones)
	}
	if facts.Milestones.OpenMilestones != milestones {
		t.Errorf("OpenMilestones = %d, want %d (count intact)", facts.Milestones.OpenMilestones, milestones)
	}
	// Members carried the bytes, so they were trimmed — far fewer listed than the
	// full membership, which cannot fit the budget.
	listed := 0
	for _, m := range facts.Milestones.Milestones {
		listed += len(m.Members)
	}
	if listed >= milestones*perMilestone {
		t.Errorf("listed members = %d, want < %d (members trimmed to fit)", listed, milestones*perMilestone)
	}
}

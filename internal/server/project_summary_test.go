package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/summary"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callProjectSummary drives the tool through the in-memory MCP session and
// returns the raw result so error-path cases can assert on IsError.
func callProjectSummary(t *testing.T, srv *mcp.Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "project_summary",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call project_summary: %v", err)
	}
	return res
}

// decodeSummary round-trips StructuredContent back into the typed orientation
// facts for assertions.
func decodeSummary(t *testing.T, res *mcp.CallToolResult) summary.Facts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts summary.Facts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("unmarshal facts: %v", err)
	}
	return facts
}

func summaryIssue(num int, ms *github.MilestoneRef, labels ...string) github.Issue {
	return github.Issue{
		Number:         num,
		Title:          "issue",
		URL:            "u",
		CreatedAt:      daysAgo(10),
		LastActivityAt: daysAgo(10),
		Labels:         labels,
		Milestone:      ms,
	}
}

// TestProjectSummaryPopulatesBlocks is the end-to-end acceptance: a fetcher
// returning issues, milestones, and PRs yields a populated summary across all
// every block present, both secondary blocks available.
func TestProjectSummaryPopulatesBlocks(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n")
	fetcher := fakeFetcher{
		result: github.IssueListResult{
			Issues: []github.Issue{
				summaryIssue(1, &github.MilestoneRef{Number: 7, Title: "R5"}, "area/net", "bug"),
				summaryIssue(2, nil, "area/net"),
			},
			TotalOpen: 2,
		},
		milestones: github.MilestoneListResult{
			Milestones: []github.Milestone{{Number: 7, Title: "R5", URL: "m7", OpenIssues: 1, ClosedIssues: 4}},
			TotalOpen:  1,
		},
		pullRequests: github.PullRequestListResult{
			PullRequests: []github.PullRequest{
				{Number: 10, Title: "pr", URL: "u10", HeadRefName: "feature/x", CIStatus: "SUCCESS", CreatedAt: daysAgo(2), LastActivityAt: daysAgo(2)},
			},
			TotalOpen: 1,
		},
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	if facts.Repo != "acme/widgets" {
		t.Errorf("Repo = %q, want acme/widgets", facts.Repo)
	}
	if !facts.Milestones.Available || len(facts.Milestones.Milestones) != 1 {
		t.Errorf("Milestones = %+v, want available with 1 milestone", facts.Milestones)
	}
	if got := facts.Milestones.Milestones[0]; got.OpenIssues != 1 || len(got.Members) != 1 {
		t.Errorf("milestone progress = %+v, want OpenIssues 1 with 1 member", got)
	}
	if len(facts.AreaInventory.Areas) != 1 || facts.AreaInventory.Areas[0].Area != "net" {
		t.Errorf("AreaInventory.Areas = %+v, want [net]", facts.AreaInventory.Areas)
	}
	if !facts.OpenPRs.Available || facts.OpenPRs.OpenPRCount != 1 {
		t.Errorf("OpenPRs = %+v, want available with 1 PR", facts.OpenPRs)
	}
	// Recommendations annotate the bug issue.
	var sawBug bool
	for _, c := range facts.Recommendations.Candidates {
		if c.Number == 1 && c.IsBug {
			sawBug = true
		}
	}
	if !sawBug {
		t.Errorf("recommendations did not flag issue 1 as a bug: %+v", facts.Recommendations.Candidates)
	}
	// The open-issue set lists the fetched open numbers (the resolvable surface for
	// a candidate's stated bodyRefs), ascending and complete on a non-truncated fetch.
	if got := facts.OpenIssueSet.Numbers; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("OpenIssueSet.Numbers = %v, want [1 2]", got)
	}
	if facts.OpenIssueSet.FetchTruncated {
		t.Error("OpenIssueSet.FetchTruncated = true, want false (whole window fetched)")
	}
}

// TestProjectSummaryOpenIssueSetUncappedByLimit is the soundness guard: the
// open-issue set is the FULL fetched window, never capped by limit. With a fetched
// window larger than the limit, an open issue sitting beyond the recommendation
// list cap must still appear in numbers — otherwise a real open blocker would read
// as ∉ set and the resolution contract would silently lie.
func TestProjectSummaryOpenIssueSetUncappedByLimit(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			summaryIssue(1, nil), summaryIssue(2, nil), summaryIssue(3, nil),
			summaryIssue(4, nil), summaryIssue(5, nil), // #5 sits beyond the limit-2 list cap
		},
		TotalOpen: 5,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 2}))

	// The recommendation list IS capped at the limit — proving the set below is not
	// derived from it.
	if !facts.Recommendations.ListTruncated || len(facts.Recommendations.Candidates) != 2 {
		t.Fatalf("recommendations not capped at 2 (got %d, truncated=%v) — test no longer guards the cap",
			len(facts.Recommendations.Candidates), facts.Recommendations.ListTruncated)
	}
	want := []int{1, 2, 3, 4, 5}
	got := facts.OpenIssueSet.Numbers
	if len(got) != len(want) {
		t.Fatalf("OpenIssueSet.Numbers = %v, want %v (full window, never limit-capped)", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("OpenIssueSet.Numbers[%d] = %d, want %d (full: %v)", i, got[i], w, got)
		}
	}
}

// TestProjectSummaryOpenIssueSetTruncated pins the truncation seam: when the fetch
// window did not cover every open issue, fetchTruncated marks numbers as a floor —
// so a ref absent from numbers cannot be read as resolved (it may sit outside the
// window).
func TestProjectSummaryOpenIssueSetTruncated(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{summaryIssue(1, nil), summaryIssue(2, nil)},
		TotalOpen: 10, // more open than fetched
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if !facts.OpenIssueSet.FetchTruncated {
		t.Error("OpenIssueSet.FetchTruncated = false, want true (window did not cover every open issue)")
	}
}

// TestProjectSummarySurfacesCriticalPath is the acceptance for the critical-path
// block: given a manifest declaring an ordered stream list and the critical-path
// label, the tool groups open critical-path-labeled issues by stream in declared
// order and reports a per-stream gate-cleared signal. A non-critical-path issue in
// a stream's area is not a member; a declared stream with no open member is
// trivially cleared.
func TestProjectSummarySurfacesCriticalPath(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n  criticalPath:\n    streams: [simulation, narrative, ui]\n    label: critical-path\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			summaryIssue(1, nil, "area/simulation", "critical-path"), // simulation member, blocks its gate
			summaryIssue(2, nil, "area/narrative", "critical-path"),  // narrative member
			summaryIssue(3, nil, "area/simulation"),                  // not critical-path → not a member
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	cp := facts.CriticalPath

	if !cp.Configured {
		t.Fatalf("CriticalPath.Configured = false, want true")
	}
	// Declared order is preserved (not count-sorted like areaInventory).
	gotOrder := []string{cp.Streams[0].Stream, cp.Streams[1].Stream, cp.Streams[2].Stream}
	wantOrder := []string{"simulation", "narrative", "ui"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("stream order = %v, want %v", gotOrder, wantOrder)
		}
	}
	// simulation has an open critical-path member → gate not cleared.
	sim := cp.Streams[0]
	if sim.GateCleared {
		t.Errorf("simulation GateCleared = true, want false (issue 1 open)")
	}
	if len(sim.Members) != 1 || sim.Members[0].Number != 1 {
		t.Errorf("simulation Members = %+v, want [#1]", sim.Members)
	}
	// ui has no critical-path member → trivially cleared.
	ui := cp.Streams[2]
	if !ui.GateCleared || len(ui.Members) != 0 {
		t.Errorf("ui = %+v, want gateCleared with no members", ui)
	}
}

// cpManifest is the shared critical-path manifest for the fetch-behavior tests
// below: an `area/` taxonomy and a two-stream critical path.
const cpManifest = "acme/widgets:\n  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n  criticalPath:\n    streams: [simulation, narrative]\n    label: critical-path\n"

// TestProjectSummaryCriticalPathAuthoritativeUnderTruncation pins the #95 fix: when
// the general open-issue window truncates (more open issues than the fetch window),
// the critical-path block is sourced from a dedicated label-scoped fetch of the
// bounded critical-path subset — so the gate is authoritative (fetchTruncated
// false) regardless of total backlog size, rather than permanently provisional.
func TestProjectSummaryCriticalPathAuthoritativeUnderTruncation(t *testing.T) {
	root := writeManifestDir(t, cpManifest)
	var labeledCalls atomic.Int64
	fetcher := fakeFetcher{
		// General window truncated: 2 of 203 fetched, neither a critical-path issue —
		// under the old design the gate read cleared-but-provisional here.
		result: github.IssueListResult{
			Issues:    []github.Issue{summaryIssue(1, nil, "area/simulation"), summaryIssue(2, nil)},
			TotalOpen: 203,
		},
		// The labeled fetch returns the complete critical-path subset.
		labeledResult: github.IssueListResult{
			Issues:    []github.Issue{summaryIssue(51, nil, "area/simulation", "critical-path")},
			TotalOpen: 1,
		},
		labeledCalls: &labeledCalls,
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	cp := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})).CriticalPath
	if cp == nil || !cp.Configured || !cp.Available {
		t.Fatalf("cp = %+v, want configured and available", cp)
	}
	if labeledCalls.Load() != 1 {
		t.Errorf("labeled fetch called %d times, want 1 (general window truncated)", labeledCalls.Load())
	}
	if cp.FetchTruncated {
		t.Errorf("FetchTruncated = true, want false (labeled subset complete despite truncated general window — the #95 fix)")
	}
	if sim := cp.Streams[0]; sim.GateCleared || len(sim.Members) != 1 || sim.Members[0].Number != 51 {
		t.Errorf("simulation = %+v, want uncleared with member #51 (from the labeled fetch)", sim)
	}
	if nar := cp.Streams[1]; !nar.GateCleared {
		t.Errorf("narrative GateCleared = false, want true (authoritative, not provisional)")
	}
}

// TestProjectSummaryCriticalPathSkipsFetchWhenWindowComplete pins the failure-surface
// floor: when the general window already covers every open issue, the block is
// computed from it with no second fetch — so no repo at or below the fetch window
// pays the extra request or its failure surface.
func TestProjectSummaryCriticalPathSkipsFetchWhenWindowComplete(t *testing.T) {
	root := writeManifestDir(t, cpManifest)
	var labeledCalls atomic.Int64
	fetcher := fakeFetcher{
		result: github.IssueListResult{
			Issues:    []github.Issue{summaryIssue(1, nil, "area/simulation", "critical-path")},
			TotalOpen: 1, // complete window
		},
		labeledCalls: &labeledCalls,
		// labeledResult intentionally empty: a wrongful fetch would clear the gate and
		// fail the member assertion below.
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	cp := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})).CriticalPath
	if labeledCalls.Load() != 0 {
		t.Errorf("labeled fetch called %d times, want 0 (general window was complete)", labeledCalls.Load())
	}
	if sim := cp.Streams[0]; sim.GateCleared || len(sim.Members) != 1 || sim.Members[0].Number != 1 {
		t.Errorf("simulation = %+v, want uncleared with member #1 (from the complete general window)", sim)
	}
}

// TestProjectSummaryCriticalPathDegradesOnFetchFailure pins C1: when the general
// window truncated and the dedicated labeled fetch fails, the block degrades
// (Available:false, Configured:true) rather than reducing over an empty set — which
// would falsely clear every gate — or failing the whole call.
func TestProjectSummaryCriticalPathDegradesOnFetchFailure(t *testing.T) {
	root := writeManifestDir(t, cpManifest)
	fetcher := fakeFetcher{
		result: github.IssueListResult{
			Issues:    []github.Issue{summaryIssue(1, nil, "area/simulation")},
			TotalOpen: 203, // truncated → triggers the labeled fetch
		},
		labeledErr: errors.New("boom"),
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	cp := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})).CriticalPath
	if cp == nil {
		t.Fatal("CriticalPath block absent; a degraded block must still be present")
	}
	if cp.Available {
		t.Errorf("Available = true, want false (labeled fetch failed → degrade)")
	}
	if cp.Unavailable != "fetch_failed" {
		t.Errorf("Unavailable = %q, want %q", cp.Unavailable, "fetch_failed")
	}
	if !cp.Configured {
		t.Errorf("Configured = false, want true (the repo is configured; only the fetch failed)")
	}
	for _, s := range cp.Streams {
		if s.GateCleared {
			t.Errorf("degraded block cleared gate %q — must not report gates on a failed fetch", s.Stream)
		}
	}
}

// TestProjectSummaryCriticalPathUnconfiguredButWanted pins M5: when the block is
// requested but the repo declares no critical path, it is still present with
// configured:false (the documented no-op contract), available, and no labeled fetch
// is issued even though the general window truncated.
func TestProjectSummaryCriticalPathUnconfiguredButWanted(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n") // no criticalPath
	var labeledCalls atomic.Int64
	fetcher := fakeFetcher{
		result:       github.IssueListResult{Issues: []github.Issue{summaryIssue(1, nil)}, TotalOpen: 203}, // truncated
		labeledCalls: &labeledCalls,
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	cp := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})).CriticalPath
	if cp == nil {
		t.Fatal("CriticalPath block absent; want present with configured:false")
	}
	if cp.Configured {
		t.Errorf("Configured = true, want false (no critical-path convention)")
	}
	if !cp.Available {
		t.Errorf("Available = false, want true (a no-op is not a degraded fetch)")
	}
	if labeledCalls.Load() != 0 {
		t.Errorf("labeled fetch called %d times, want 0 (unconfigured ⇒ no fetch)", labeledCalls.Load())
	}
}

// TestProjectSummaryCriticalPathSurfacesLabelTruncation is the outside-in pin for the
// gate's second provisional axis: an on-path member whose own labels were capped
// (LabelsTruncated) surfaces LabelTruncatedCount > 0 through the tool output, so a
// cleared gate reads provisional. The member's area label is truncated out, so it
// lands unareaed and its streams read cleared — the exact silent false-clear the count
// guards. A complete window ⇒ no labeled fetch, so the signal rides the general path.
func TestProjectSummaryCriticalPathSurfacesLabelTruncation(t *testing.T) {
	root := writeManifestDir(t, cpManifest)
	capped := summaryIssue(1, nil, "critical-path") // area label truncated out ⇒ unareaed
	capped.LabelsTruncated = true
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{capped},
		TotalOpen: 1, // complete window ⇒ general path, no labeled fetch
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	cp := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})).CriticalPath
	if cp == nil || !cp.Configured {
		t.Fatalf("cp = %+v, want configured", cp)
	}
	if cp.LabelTruncatedCount != 1 {
		t.Errorf("LabelTruncatedCount = %d, want 1 (the capped on-path member)", cp.LabelTruncatedCount)
	}
	if cp.UnareaedCount != 1 {
		t.Errorf("UnareaedCount = %d, want 1 (member lost its area label)", cp.UnareaedCount)
	}
	// Both streams read cleared (no assigned member), but LabelTruncatedCount marks
	// that clearance provisional — the hidden member could belong to either.
	for _, s := range cp.Streams {
		if !s.GateCleared {
			t.Errorf("stream %q GateCleared = false, want true (member is unareaed)", s.Stream)
		}
	}
}

// TestProjectSummarySurfacesDependencyClassification pins the classification-only
// dependency block on the orientation read: the graph-level ready/blocked split
// and the gate set (each gate's downstream count), without the raw per-issue edges
// the recommendations block already ships.
func TestProjectSummarySurfacesDependencyClassification(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")

	capstone := issue(7, daysAgo(1))
	capstone.BlockedBy = []github.DependencyRef{{Number: 42, Open: true}, {Number: 43, Open: true}}
	gateA := issue(42, daysAgo(1))
	gateA.Blocking = []github.DependencyRef{{Number: 7, Open: true}}
	gateB := issue(43, daysAgo(1))
	gateB.Blocking = []github.DependencyRef{{Number: 7, Open: true}}
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{capstone, gateA, gateB}, TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	dep := facts.Dependencies
	if dep == nil {
		t.Fatal("Dependencies block absent; want present on the full composite")
	}
	if dep.BlockedCount != 1 || dep.ReadyCount != 2 || dep.GateCount != 2 {
		t.Errorf("counts blocked=%d ready=%d gate=%d, want 1/2/2", dep.BlockedCount, dep.ReadyCount, dep.GateCount)
	}
	if len(dep.Gates) != 2 {
		t.Fatalf("Gates = %d, want 2", len(dep.Gates))
	}
	for _, g := range dep.Gates {
		if g.BlockingCount != 1 {
			t.Errorf("gate #%d BlockingCount = %d, want 1 (unblocks #7)", g.Number, g.BlockingCount)
		}
	}
}

// TestProjectSummaryDegradesMilestonesOnFetchError pins that a milestone
// sub-fetch failure degrades only that block — not a tool error — while the
// issue-derived blocks stay populated.
func TestProjectSummaryDegradesMilestonesOnFetchError(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{
		result:        github.IssueListResult{Issues: []github.Issue{summaryIssue(1, nil)}, TotalOpen: 1},
		milestonesErr: github.ErrRepoNotFound, // a non-rate-limit failure
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.Milestones.Available {
		t.Error("Milestones.Available = true, want false (sub-fetch failed)")
	}
	if facts.Milestones.Unavailable != "fetch_failed" {
		t.Errorf("Milestones.Unavailable = %q, want fetch_failed", facts.Milestones.Unavailable)
	}
	// The issue-derived blocks are unaffected.
	if facts.Hygiene.OpenIssueCount != 1 {
		t.Errorf("Hygiene.OpenIssueCount = %d, want 1 (issue fetch succeeded)", facts.Hygiene.OpenIssueCount)
	}
}

// TestProjectSummaryDegradesPRsOnFetchError pins the same degradation for the PR
// sub-fetch.
func TestProjectSummaryDegradesPRsOnFetchError(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{
		result:         github.IssueListResult{Issues: []github.Issue{summaryIssue(1, nil)}, TotalOpen: 1},
		pullRequestErr: github.ErrRepoNotFound,
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.OpenPRs.Available || facts.OpenPRs.Unavailable != "fetch_failed" {
		t.Errorf("OpenPRs = %+v, want unavailable/fetch_failed", facts.OpenPRs)
	}
}

// TestProjectSummaryPrimaryFetchErrorIsToolError pins that the open-issue fetch
// is primary: its failure fails the whole call (IsError), not a degraded block.
func TestProjectSummaryPrimaryFetchErrorIsToolError(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{err: github.ErrRepoNotFound}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})
	if !res.IsError {
		t.Error("IsError = false, want true (primary issue fetch failed)")
	}
}

// TestProjectSummaryRecommendationBodyRefsEmptySerializesAsArray pins the non-nil
// convention through the JSON round-trip: a recommendation candidate with no body
// references must serialize bodyRefs as [], not null, so a client never sees a
// null it has to special-case. Parse-correctness (self/dup/PR exclusion) is pinned
// by the summary unit test; this case is scoped to the [] vs null serialization
// contract only.
func TestProjectSummaryRecommendationBodyRefsEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	noRefs := summaryIssue(1, nil)
	noRefs.BodyText = "Ready to start; no dependencies."
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noRefs},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Recommendations.Candidates) != 1 {
		t.Fatalf("listed %d candidates, want 1", len(facts.Recommendations.Candidates))
	}
	// After a JSON round-trip, `[]` decodes to a non-nil empty slice and `null` to
	// nil — so a non-nil slice here proves the encoder emitted [].
	if facts.Recommendations.Candidates[0].BodyRefs == nil {
		t.Error("BodyRefs = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

// TestProjectSummaryRecommendationSurfacesNativeBlockedBy pins the authoritative
// dependency signal (#60): each candidate carries the OPEN native blocked-by edge
// numbers — ascending, closed blockers omitted — so a caller ranking "what to start
// next" can trust whether a candidate is actually blocked, distinct from the
// heuristic bodyRefs. A window-truncated edge set sets blockedByTruncated.
func TestProjectSummaryRecommendationSurfacesNativeBlockedBy(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	blocked := summaryIssue(1, nil)
	blocked.BlockedBy = []github.DependencyRef{
		{Number: 11, Open: true},
		{Number: 7, Open: true},
		{Number: 9, Open: false}, // closed blocker no longer gates — excluded
	}
	blocked.BlockedByTruncated = true
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{blocked},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Recommendations.Candidates) != 1 {
		t.Fatalf("listed %d candidates, want 1", len(facts.Recommendations.Candidates))
	}
	c := facts.Recommendations.Candidates[0]
	if len(c.BlockedBy) != 2 || c.BlockedBy[0] != 7 || c.BlockedBy[1] != 11 {
		t.Errorf("BlockedBy = %v, want [7 11] (open only, ascending)", c.BlockedBy)
	}
	if !c.BlockedByTruncated {
		t.Error("BlockedByTruncated = false, want true (edge set exceeded the fetch window)")
	}
}

// TestProjectSummaryRecommendationBlockedByEmptySerializesAsArray pins the non-nil
// convention for the native signal through the JSON round-trip, mirroring bodyRefs.
func TestProjectSummaryRecommendationBlockedByEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	noBlockers := summaryIssue(1, nil)
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noBlockers},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Recommendations.Candidates) != 1 {
		t.Fatalf("listed %d candidates, want 1", len(facts.Recommendations.Candidates))
	}
	if facts.Recommendations.Candidates[0].BlockedBy == nil {
		t.Error("BlockedBy = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

// TestProjectSummaryRecommendationSurfacesNativeBlocking pins the reverse-direction
// authoritative signal (#60): each candidate carries the OPEN native blocking edge
// numbers — ascending, closed downstream issues omitted — so a caller weighing "what
// to start next" sees how much downstream work a candidate gates, distinct from
// blockedBy (whether the candidate is itself blocked). A window-truncated edge set
// sets blockingTruncated.
func TestProjectSummaryRecommendationSurfacesNativeBlocking(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	blocked := summaryIssue(1, nil)
	blocked.Blocking = []github.DependencyRef{
		{Number: 21, Open: true},
		{Number: 17, Open: true},
		{Number: 19, Open: false}, // closed downstream issue no longer gated — excluded
	}
	blocked.BlockingTruncated = true
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{blocked},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Recommendations.Candidates) != 1 {
		t.Fatalf("listed %d candidates, want 1", len(facts.Recommendations.Candidates))
	}
	c := facts.Recommendations.Candidates[0]
	if len(c.Blocking) != 2 || c.Blocking[0] != 17 || c.Blocking[1] != 21 {
		t.Errorf("Blocking = %v, want [17 21] (open only, ascending)", c.Blocking)
	}
	if !c.BlockingTruncated {
		t.Error("BlockingTruncated = false, want true (edge set exceeded the fetch window)")
	}
}

// TestProjectSummaryRecommendationBlockingEmptySerializesAsArray pins the non-nil
// convention for the reverse-direction signal through the JSON round-trip, mirroring
// blockedBy.
func TestProjectSummaryRecommendationBlockingEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	noBlocking := summaryIssue(1, nil)
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noBlocking},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Recommendations.Candidates) != 1 {
		t.Fatalf("listed %d candidates, want 1", len(facts.Recommendations.Candidates))
	}
	if facts.Recommendations.Candidates[0].Blocking == nil {
		t.Error("Blocking = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

// TestProjectSummaryRecommendationSurfacesReadyBlockerOfPrioritized is the #97
// acceptance: a ready issue that gates a milestoned issue — but is not itself a
// bug, aged, or low-numbered — still reaches the caller. It survives the candidate
// cap (the reserve) and carries gatesPrioritized naming the prioritized work it
// unblocks, so orientation can point at the do-first ready work rather than only
// naming the blocked priority.
func TestProjectSummaryRecommendationSurfacesReadyBlockerOfPrioritized(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	// The prioritized work: a milestoned issue, itself blocked.
	priority := summaryIssue(1, &github.MilestoneRef{Number: 7, Title: "Round 5"})
	priority.BlockedBy = []github.DependencyRef{{Number: 90, Open: true}}
	// The ready blocker: freshly filed (high number, recent), not a bug, gating the
	// milestoned issue. Under the old oldest-first cap it would sink below the list.
	blocker := github.Issue{
		Number: 90, Title: "ready blocker", URL: "u90",
		CreatedAt: daysAgo(1), LastActivityAt: daysAgo(1),
		Blocking: []github.DependencyRef{{Number: 1, Open: true}},
	}
	// Aged filler that would otherwise win the cap on age.
	filler := github.Issue{Number: 40, Title: "aged", URL: "u40", CreatedAt: daysAgo(300), LastActivityAt: daysAgo(300)}
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{priority, blocker, filler},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": float64(2)}))
	var got *summary.RecommendationCandidate
	for i := range facts.Recommendations.Candidates {
		if facts.Recommendations.Candidates[i].Number == 90 {
			got = &facts.Recommendations.Candidates[i]
		}
	}
	if got == nil {
		t.Fatalf("ready blocker #90 absent from candidates %+v — did not survive the cap", facts.Recommendations.Candidates)
	}
	if len(got.GatesPrioritized) != 1 || got.GatesPrioritized[0] != 1 {
		t.Errorf("GatesPrioritized = %v, want [1] (the milestoned issue it unblocks)", got.GatesPrioritized)
	}
}

func TestProjectSummaryRecommendationSurfacesNativeSubIssues(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	parent := summaryIssue(1, nil)
	parent.SubIssues = []github.DependencyRef{
		{Number: 27, Open: true},
		{Number: 23, Open: true},
		{Number: 25, Open: false}, // completed child no longer gates — excluded
	}
	parent.SubIssuesTruncated = true
	// total minus completed (6-3=3) exceeds the 2 listed open children — the
	// authoritative pair counts cross-repo and beyond-window children the list drops.
	parent.SubIssuesTotal = 6
	parent.SubIssuesCompleted = 3
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{parent},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Recommendations.Candidates) != 1 {
		t.Fatalf("listed %d candidates, want 1", len(facts.Recommendations.Candidates))
	}
	c := facts.Recommendations.Candidates[0]
	if len(c.SubIssues) != 2 || c.SubIssues[0] != 23 || c.SubIssues[1] != 27 {
		t.Errorf("SubIssues = %v, want [23 27] (open only, ascending)", c.SubIssues)
	}
	if !c.SubIssuesTruncated {
		t.Error("SubIssuesTruncated = false, want true (child set exceeded the fetch window)")
	}
	if c.SubIssuesTotal != 6 || c.SubIssuesCompleted != 3 {
		t.Errorf("completion = %d/%d, want 3/6 (completed/total)", c.SubIssuesCompleted, c.SubIssuesTotal)
	}
}

// TestProjectSummaryRecommendationSubIssuesEmptySerializesAsArray pins the non-nil
// convention for the hierarchy signal through the JSON round-trip, mirroring
// blockedBy/blocking.
func TestProjectSummaryRecommendationSubIssuesEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  summary:\n    bugLabels: [bug]\n")
	noChildren := summaryIssue(1, nil)
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noChildren},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeSummary(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Recommendations.Candidates) != 1 {
		t.Fatalf("listed %d candidates, want 1", len(facts.Recommendations.Candidates))
	}
	if facts.Recommendations.Candidates[0].SubIssues == nil {
		t.Error("SubIssues = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

// TestProjectSummaryRequiresOwnerRepo pins the input validation.
func TestProjectSummaryRequiresOwnerRepo(t *testing.T) {
	srv := New(WithFetcher(fakeFetcher{}), WithClock(func() time.Time { return fixedClock }))
	res := callProjectSummary(t, srv, map[string]any{"owner": "  ", "repo": "widgets"})
	if !res.IsError {
		t.Error("IsError = false, want true (blank owner)")
	}
}

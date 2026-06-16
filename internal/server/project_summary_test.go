package server

import (
	"context"
	"encoding/json"
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
// five blocks, both secondary blocks available.
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

// TestProjectSummaryRequiresOwnerRepo pins the input validation.
func TestProjectSummaryRequiresOwnerRepo(t *testing.T) {
	srv := New(WithFetcher(fakeFetcher{}), WithClock(func() time.Time { return fixedClock }))
	res := callProjectSummary(t, srv, map[string]any{"owner": "  ", "repo": "widgets"})
	if !res.IsError {
		t.Error("IsError = false, want true (blank owner)")
	}
}

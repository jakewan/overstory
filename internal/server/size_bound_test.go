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

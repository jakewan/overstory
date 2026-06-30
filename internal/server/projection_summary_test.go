package server

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// summaryProjectionManifest + fetcher populate every projectable summary block,
// both secondary fetches succeeding.
const summaryProjectionManifest = "acme/widgets:\n" +
	"  summary:\n    bugLabels: [bug]\n" +
	"  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n" +
	"  criticalPath:\n    streams: [simulation]\n    label: critical-path\n"

func summaryProjectionFetcher() fakeFetcher {
	return fakeFetcher{
		result: github.IssueListResult{
			Issues: []github.Issue{
				summaryIssue(1, &github.MilestoneRef{Number: 7, Title: "R5"}, "area/simulation", "bug", "critical-path"),
				summaryIssue(2, nil, "area/simulation"),
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
}

// TestProjectSummaryProjectionSubset pins the core contract for the orientation
// read: one requested block plus meta blocks, every other content block omitted.
func TestProjectSummaryProjectionSubset(t *testing.T) {
	root := writeManifestDir(t, summaryProjectionManifest)
	srv := New(WithFetcher(summaryProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"areaInventory"}})
	assertKeys(t, topLevelKeys(t, res),
		[]string{"areaInventory", "openIssueSet", "repo", "generatedAt"},
		[]string{"milestones", "hygiene", "openPRs", "recommendations", "criticalPath"},
	)
}

// TestProjectSummaryProjectionDefaultFullComposite pins that an absent parameter
// returns every content block.
func TestProjectSummaryProjectionDefaultFullComposite(t *testing.T) {
	root := writeManifestDir(t, summaryProjectionManifest)
	srv := New(WithFetcher(summaryProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})
	assertKeys(t, topLevelKeys(t, res), summaryBlockNames, nil)
}

// TestProjectSummaryProjectionSkipsSecondaryFetch pins that a primary-only
// allowlist runs neither the milestone nor PR fetch and does not panic despite the
// now-pointer blocks (the milestone member-trim loop is the easiest nil-deref to
// miss).
func TestProjectSummaryProjectionSkipsSecondaryFetch(t *testing.T) {
	root := writeManifestDir(t, summaryProjectionManifest)
	f := summaryProjectionFetcher()
	f.milestoneCalls = &atomic.Int64{}
	f.pullRequestCalls = &atomic.Int64{}
	srv := New(WithFetcher(f), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"areaInventory"}})
	assertKeys(t, topLevelKeys(t, res), []string{"areaInventory"}, []string{"milestones", "openPRs"})

	if n := f.milestoneCalls.Load(); n != 0 {
		t.Errorf("milestone fetch ran %d times, want 0 (milestones not requested)", n)
	}
	if n := f.pullRequestCalls.Load(); n != 0 {
		t.Errorf("pull-request fetch ran %d times, want 0 (openPRs not requested)", n)
	}
}

// TestProjectSummaryProjectionRunsRequestedSecondaryFetch is the positive control:
// requesting milestones runs that fetch (not the PR fetch) and returns the block.
func TestProjectSummaryProjectionRunsRequestedSecondaryFetch(t *testing.T) {
	root := writeManifestDir(t, summaryProjectionManifest)
	f := summaryProjectionFetcher()
	f.milestoneCalls = &atomic.Int64{}
	f.pullRequestCalls = &atomic.Int64{}
	srv := New(WithFetcher(f), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"milestones"}})
	assertKeys(t, topLevelKeys(t, res), []string{"milestones"}, []string{"openPRs", "areaInventory"})

	if n := f.milestoneCalls.Load(); n != 1 {
		t.Errorf("milestone fetch ran %d times, want 1", n)
	}
	if n := f.pullRequestCalls.Load(); n != 0 {
		t.Errorf("pull-request fetch ran %d times, want 0 (openPRs not requested)", n)
	}
}

// TestProjectSummaryProjectionRejectsUnknownBlock pins the actionable-error path.
func TestProjectSummaryProjectionRejectsUnknownBlock(t *testing.T) {
	root := writeManifestDir(t, summaryProjectionManifest)
	srv := New(WithFetcher(summaryProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"nope"}})
	if !res.IsError {
		t.Fatalf("expected tool error for unknown block name, got success: %s", contentText(res))
	}
}

// TestProjectSummaryBlockNameTagBijection guards block-name↔JSON-key drift.
func TestProjectSummaryBlockNameTagBijection(t *testing.T) {
	root := writeManifestDir(t, summaryProjectionManifest)
	srv := New(WithFetcher(summaryProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	keys := topLevelKeys(t, callProjectSummary(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	assertBijection(t, keys, summaryBlockNames)
}

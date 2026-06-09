package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/backlog"
	"github.com/jakewan/overstory/internal/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeFetcher is a static IssueFetcher: the seam that lets the acceptance test
// exercise backlog_review end-to-end without invoking gh or the network.
type fakeFetcher struct {
	result github.IssueListResult
	err    error
}

func (f fakeFetcher) ListOpenIssues(_ context.Context, _ string, _ int) (github.IssueListResult, error) {
	return f.result, f.err
}

// fixedClock is the injected wall clock; staleness is deterministic under it.
var fixedClock = time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time { return fixedClock.AddDate(0, 0, -n) }

// writeManifestDir writes a single manifest file into a fresh manifests.d under
// t.TempDir() and returns that directory, isolating discovery from the real
// ~/.config.
func writeManifestDir(t *testing.T, contents string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "manifests.d")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir manifests.d: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "repos.yml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return root
}

// callBacklogReview drives the tool through the in-memory MCP session and
// decodes the structured facts. It fails the test on transport errors but
// returns the raw result so error-path cases can assert on IsError.
func callBacklogReview(t *testing.T, srv *mcp.Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "backlog_review",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call backlog_review: %v", err)
	}
	return res
}

// decodeFacts round-trips StructuredContent (delivered to the client as untyped
// JSON) back into the typed reduction result for assertions.
func decodeFacts(t *testing.T, res *mcp.CallToolResult) backlog.StalenessFacts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts backlog.StalenessFacts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("unmarshal facts: %v", err)
	}
	return facts
}

func contentText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func issue(num int, lastActivity time.Time) github.Issue {
	return github.Issue{
		Number:         num,
		Title:          "issue",
		URL:            "https://example.com",
		CreatedAt:      daysAgo(365),
		LastActivityAt: lastActivity,
	}
}

// TestBacklogReviewUsesManifestThreshold is the outside-in anchor: given a repo
// with a manifest entry, the tool reduces its open issues to staleness facts
// using that repo's configured threshold, and reports the threshold's source.
func TestBacklogReviewUsesManifestThreshold(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 45\n    fetchLimit: 100\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			issue(1, daysAgo(100)), // stale
			issue(2, daysAgo(10)),  // fresh
			issue(3, daysAgo(50)),  // stale
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	if facts.Repo != "acme/widgets" {
		t.Errorf("Repo = %q, want acme/widgets", facts.Repo)
	}
	if facts.ThresholdDays != 45 {
		t.Errorf("ThresholdDays = %d, want 45", facts.ThresholdDays)
	}
	if facts.ThresholdSource != "manifest" {
		t.Errorf("ThresholdSource = %q, want manifest", facts.ThresholdSource)
	}
	if facts.OpenIssueCount != 3 {
		t.Errorf("OpenIssueCount = %d, want 3", facts.OpenIssueCount)
	}
	if facts.StaleCount != 2 {
		t.Errorf("StaleCount = %d, want 2", facts.StaleCount)
	}
	if facts.FreshCount != 1 {
		t.Errorf("FreshCount = %d, want 1", facts.FreshCount)
	}
}

// TestBacklogReviewFallsBackToDefaults pins the no-manifest path: a repo without
// an entry uses the generic default threshold (30 days), reported as "default".
func TestBacklogReviewFallsBackToDefaults(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{issue(1, daysAgo(40))}, // stale at 30, fresh at 45
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "other", "repo": "thing"}))

	if facts.ThresholdDays != 30 {
		t.Errorf("ThresholdDays = %d, want 30", facts.ThresholdDays)
	}
	if facts.ThresholdSource != "default" {
		t.Errorf("ThresholdSource = %q, want default", facts.ThresholdSource)
	}
	if facts.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1 (40d inactive >= 30d default)", facts.StaleCount)
	}
}

// TestBacklogReviewRepoNotFound pins the error path: a fetch failure surfaces as
// a tool error (IsError) whose message names the repo, not any manifest file.
func TestBacklogReviewRepoNotFound(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	fetcher := fakeFetcher{err: github.ErrRepoNotFound}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "ghost", "repo": "missing"})

	if !res.IsError {
		t.Fatalf("IsError = false, want true for repo-not-found")
	}
	if msg := contentText(res); !strings.Contains(msg, "ghost/missing") {
		t.Errorf("error message %q does not name the repo ghost/missing", msg)
	}
}

// TestBacklogReviewSurfacesTruncation pins the never-silently-truncate contract:
// openIssueCount stays exact (from totalOpen) while both the list and fetch
// truncation seams are reported.
func TestBacklogReviewSurfacesTruncation(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			issue(1, daysAgo(100)),
			issue(2, daysAgo(90)),
			issue(3, daysAgo(80)),
		},
		TotalOpen: 500, // far more open than we fetched
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{
		"owner": "acme", "repo": "widgets", "limit": float64(2),
	}))

	if facts.OpenIssueCount != 500 {
		t.Errorf("OpenIssueCount = %d, want 500 (exact)", facts.OpenIssueCount)
	}
	if !facts.FetchTruncated {
		t.Errorf("FetchTruncated = false, want true (fetched 3 of 500)")
	}
	if !facts.ListTruncated {
		t.Errorf("ListTruncated = false, want true (3 stale, limit 2)")
	}
	if len(facts.StaleIssues) != 2 {
		t.Errorf("listed %d stale issues, want 2 (the limit)", len(facts.StaleIssues))
	}
}

// TestBacklogReviewAppliesLimitDefault pins that the hand-written input schema's
// default (20) is applied when limit is omitted — the defaults-apply path that
// would otherwise need a null-arguments guard.
func TestBacklogReviewAppliesLimitDefault(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{issue(1, daysAgo(100))},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	if facts.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (schema default)", facts.Limit)
	}
}

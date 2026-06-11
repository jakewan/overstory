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

// writeManifestFiles writes several named manifest files into a fresh
// manifests.d, for cases where discovery across multiple files matters.
func writeManifestFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "manifests.d")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir manifests.d: %v", err)
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write manifest %s: %v", name, err)
		}
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
// JSON) back into the typed reduction result for assertions. The tool returns
// the composite BacklogFacts: review-level identity (repo, generatedAt) at the
// top, each grooming signal under its own block.
func decodeFacts(t *testing.T, res *mcp.CallToolResult) backlog.Facts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts backlog.Facts
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

func deferredIssue(num int, lastActivity time.Time, labels ...string) github.Issue {
	is := issue(num, lastActivity)
	is.Labels = labels
	return is
}

// labeledIssue builds an issue with labels for the area-balance reduction, which
// is time-independent (the lastActivity is irrelevant here).
func labeledIssue(num int, labels ...string) github.Issue {
	return deferredIssue(num, daysAgo(1), labels...)
}

// TestBacklogReviewSurfacesAreaBalance pins the area-balance grooming signal:
// given a manifest declaring area prefixes and explicit labels, the tool
// distributes open issues across areas, counts the unclassified and multi-area
// issues, and orders areas by count — alongside the other reduction blocks.
func TestBacklogReviewSurfacesAreaBalance(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  areaBalance:\n    labels: [http]\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			labeledIssue(1, "area/networking"),
			labeledIssue(2, "area/networking", "area/storage"), // multi-area
			labeledIssue(3, "http"),                            // explicit list
			labeledIssue(4, "bug"),                             // unclassified
		},
		TotalOpen: 4,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	ab := facts.AreaBalance

	if ab.Unclassified != 1 {
		t.Errorf("Unclassified = %d, want 1", ab.Unclassified)
	}
	if ab.MultiAreaCount != 1 {
		t.Errorf("MultiAreaCount = %d, want 1 (issue 2 spans two areas)", ab.MultiAreaCount)
	}
	// networking=2, storage=1, http=1 → ordered by count desc, tie-break by name.
	want := []backlog.AreaCount{{Area: "networking", Count: 2}, {Area: "http", Count: 1}, {Area: "storage", Count: 1}}
	if len(ab.Areas) != len(want) {
		t.Fatalf("Areas = %+v, want %+v", ab.Areas, want)
	}
	for i, w := range want {
		if ab.Areas[i] != w {
			t.Errorf("Areas[%d] = %+v, want %+v", i, ab.Areas[i], w)
		}
	}
	// The other blocks still reduce the same window.
	if facts.Staleness.OpenIssueCount != 4 {
		t.Errorf("Staleness.OpenIssueCount = %d, want 4", facts.Staleness.OpenIssueCount)
	}
}

// TestBacklogReviewAreaBalanceGenericDefault pins the out-of-box behavior: a repo
// with no areaBalance block still classifies common `area/*` labels via the
// generic default prefixes.
func TestBacklogReviewAreaBalanceGenericDefault(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{labeledIssue(1, "area/api"), labeledIssue(2, "area/api")},
		TotalOpen: 2,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	ab := facts.AreaBalance

	if len(ab.Areas) != 1 || ab.Areas[0].Area != "api" || ab.Areas[0].Count != 2 {
		t.Errorf("Areas = %+v, want [{api 2}] (default area/ prefix)", ab.Areas)
	}
	if ab.Unclassified != 0 {
		t.Errorf("Unclassified = %d, want 0", ab.Unclassified)
	}
}

// TestBacklogReviewAreaBalanceAllUnclassified pins that issues carrying no
// recognized area label land in the unclassified bucket rather than being
// dropped.
func TestBacklogReviewAreaBalanceAllUnclassified(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{labeledIssue(1, "bug"), labeledIssue(2, "enhancement")},
		TotalOpen: 2,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	ab := facts.AreaBalance

	if len(ab.Areas) != 0 {
		t.Errorf("Areas = %+v, want empty", ab.Areas)
	}
	if ab.Unclassified != 2 {
		t.Errorf("Unclassified = %d, want 2", ab.Unclassified)
	}
}

// qualityIssue builds an issue with a body and labels for the quality reduction.
func qualityIssue(num int, body string, labels ...string) github.Issue {
	is := labeledIssue(num, labels...)
	is.BodyText = body
	return is
}

// TestBacklogReviewSurfacesQuality pins the quality grooming signal: given a
// manifest declaring a body-length bar and a required label category, the tool
// flags issues with thin bodies, no labels, or a missing category — with the
// per-check counts and granular, most-incomplete-first list a caller renders.
func TestBacklogReviewSurfacesQuality(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  quality:\n    minBodyLength: 10\n    requiredCategories:\n      - name: type\n        prefixes:\n          - prefix: type\n            delimiter: \"/\"\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			qualityIssue(1, "a thorough description here", "type/bug"), // passes all
			qualityIssue(2, "short", "type/bug"),                       // thin body
			qualityIssue(3, "a thorough description here"),             // no labels + missing type
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	q := facts.Quality

	if q.MinBodyLength != 10 {
		t.Errorf("MinBodyLength = %d, want 10", q.MinBodyLength)
	}
	if !q.CategoriesConfigured {
		t.Error("CategoriesConfigured = false, want true (category declared)")
	}
	if q.MissingBodyCount != 1 {
		t.Errorf("MissingBodyCount = %d, want 1 (issue 2)", q.MissingBodyCount)
	}
	if q.NoLabelsCount != 1 {
		t.Errorf("NoLabelsCount = %d, want 1 (issue 3)", q.NoLabelsCount)
	}
	if q.MissingCategoryCounts["type"] != 1 {
		t.Errorf("MissingCategoryCounts[type] = %d, want 1 (issue 3)", q.MissingCategoryCounts["type"])
	}
	if q.FlaggedCount != 2 {
		t.Errorf("FlaggedCount = %d, want 2 (issues 2, 3)", q.FlaggedCount)
	}
	// Most-incomplete first: issue 3 (no labels + missing type) before issue 2 (thin body).
	if len(q.FlaggedIssues) != 2 || q.FlaggedIssues[0].Number != 3 {
		t.Errorf("FlaggedIssues = %+v, want issue 3 first", q.FlaggedIssues)
	}
	// The other blocks still reduce the same window.
	if facts.Staleness.OpenIssueCount != 3 {
		t.Errorf("Staleness.OpenIssueCount = %d, want 3", facts.Staleness.OpenIssueCount)
	}
}

// TestBacklogReviewQualityFallsBackToDefaults pins the out-of-box behavior: a repo
// with no quality block still runs the universal checks (non-empty body, has
// labels), with the per-category check reported as not-configured.
func TestBacklogReviewQualityFallsBackToDefaults(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			qualityIssue(1, "", "bug"),        // empty body (default minBodyLength 1)
			qualityIssue(2, "real body desc"), // no labels
		},
		TotalOpen: 2,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	q := facts.Quality

	if q.MinBodyLength != 1 {
		t.Errorf("MinBodyLength = %d, want 1 (default)", q.MinBodyLength)
	}
	if q.CategoriesConfigured {
		t.Error("CategoriesConfigured = true, want false (none declared)")
	}
	if q.MissingBodyCount != 1 {
		t.Errorf("MissingBodyCount = %d, want 1 (issue 1 empty)", q.MissingBodyCount)
	}
	if q.NoLabelsCount != 1 {
		t.Errorf("NoLabelsCount = %d, want 1 (issue 2)", q.NoLabelsCount)
	}
}

// TestBacklogReviewSurfacesDeferred pins the deferred-issue grooming signal:
// given a repo whose manifest declares deferred labels, the tool surfaces the
// open issues carrying any of them — alongside the staleness block, since both
// reductions run over the same fetched window.
func TestBacklogReviewSurfacesDeferred(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred, blocked]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			deferredIssue(1, daysAgo(100), "blocked"),
			deferredIssue(2, daysAgo(10)), // not deferred
			deferredIssue(3, daysAgo(50), "deferred"),
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	if !facts.Deferred.Configured {
		t.Error("Deferred.Configured = false, want true (labels declared)")
	}
	if facts.Deferred.DeferredCount != 2 {
		t.Errorf("DeferredCount = %d, want 2", facts.Deferred.DeferredCount)
	}
	if len(facts.Deferred.DeferredIssues) != 2 {
		t.Fatalf("listed %d deferred issues, want 2", len(facts.Deferred.DeferredIssues))
	}
	// Most-inactive first: issue 1 (100d) before issue 3 (50d).
	if facts.Deferred.DeferredIssues[0].Number != 1 || facts.Deferred.DeferredIssues[1].Number != 3 {
		t.Errorf("order = [%d,%d], want [1,3] (most-inactive first)",
			facts.Deferred.DeferredIssues[0].Number, facts.Deferred.DeferredIssues[1].Number)
	}
	if got := facts.Deferred.DeferredIssues[0].MatchedLabels; len(got) != 1 || got[0] != "blocked" {
		t.Errorf("issue 1 MatchedLabels = %v, want [blocked]", got)
	}
	// The staleness block still reduces the same window.
	if facts.Staleness.StaleCount != 2 {
		t.Errorf("Staleness.StaleCount = %d, want 2 (deferred reduction does not disturb staleness)", facts.Staleness.StaleCount)
	}
}

// TestBacklogReviewDeferredNotConfigured pins the no-convention path: a repo
// whose manifest declares no deferred labels reports the block as not-configured
// — an empty, honest no-op rather than a guess or an error.
func TestBacklogReviewDeferredNotConfigured(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{deferredIssue(1, daysAgo(100), "blocked")},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	if facts.Deferred.Configured {
		t.Error("Deferred.Configured = true, want false (no labels declared)")
	}
	if facts.Deferred.DeferredCount != 0 {
		t.Errorf("DeferredCount = %d, want 0 (not configured)", facts.Deferred.DeferredCount)
	}
	// Staleness still works in the absence of a deferred convention.
	if facts.Staleness.StaleCount != 1 {
		t.Errorf("Staleness.StaleCount = %d, want 1", facts.Staleness.StaleCount)
	}
}

// TestBacklogReviewDeferredCaseInsensitive pins that label matching ignores
// case: a configured "deferred" matches an issue labeled "DEFERRED".
func TestBacklogReviewDeferredCaseInsensitive(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{deferredIssue(1, daysAgo(100), "DEFERRED")},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	if facts.Deferred.DeferredCount != 1 {
		t.Errorf("DeferredCount = %d, want 1 (case-insensitive match)", facts.Deferred.DeferredCount)
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
	if facts.Staleness.ThresholdDays != 45 {
		t.Errorf("ThresholdDays = %d, want 45", facts.Staleness.ThresholdDays)
	}
	if facts.Staleness.ThresholdSource != "manifest" {
		t.Errorf("ThresholdSource = %q, want manifest", facts.Staleness.ThresholdSource)
	}
	if facts.Staleness.OpenIssueCount != 3 {
		t.Errorf("OpenIssueCount = %d, want 3", facts.Staleness.OpenIssueCount)
	}
	if facts.Staleness.StaleCount != 2 {
		t.Errorf("StaleCount = %d, want 2", facts.Staleness.StaleCount)
	}
	if facts.Staleness.FreshCount != 1 {
		t.Errorf("FreshCount = %d, want 1", facts.Staleness.FreshCount)
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

	if facts.Staleness.ThresholdDays != 30 {
		t.Errorf("ThresholdDays = %d, want 30", facts.Staleness.ThresholdDays)
	}
	if facts.Staleness.ThresholdSource != "default" {
		t.Errorf("ThresholdSource = %q, want default", facts.Staleness.ThresholdSource)
	}
	if facts.Staleness.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1 (40d inactive >= 30d default)", facts.Staleness.StaleCount)
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

// TestBacklogReviewRateLimitedSurfacesResetTime pins the recovery-signal path: a
// throttle still surfaces as a tool error naming the repo, but when the typed
// rate-limit error carries a reset signal the message names the absolute instant
// the caller can retry at. A relative RetryAfter is resolved against the server
// clock; an absent or already-elapsed signal degrades to the plain message with
// no fabricated time.
func TestBacklogReviewRateLimitedSurfacesResetTime(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	for _, tc := range []struct {
		name     string
		err      error
		wantTime string // RFC3339 the message must name; "" means no time named
	}{
		{"absolute resetAt", github.RateLimitedError{ResetAt: fixedClock.Add(15 * time.Minute)}, "2026-06-09T00:15:00Z"},
		{"relative retryAfter resolved against clock", github.RateLimitedError{RetryAfter: 60 * time.Second}, "2026-06-09T00:01:00Z"},
		{"no signal", github.RateLimitedError{}, ""},
		{"elapsed resetAt clamped", github.RateLimitedError{ResetAt: fixedClock.Add(-5 * time.Minute)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := fakeFetcher{err: tc.err}
			srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

			res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})

			if !res.IsError {
				t.Fatalf("IsError = false, want true for a throttle")
			}
			msg := contentText(res)
			if !strings.Contains(msg, "acme/widgets") {
				t.Errorf("message %q does not name the repo", msg)
			}
			if tc.wantTime == "" {
				if strings.Contains(msg, "retry after") {
					t.Errorf("message %q names a retry time, want none", msg)
				}
				return
			}
			if !strings.Contains(msg, "retry after "+tc.wantTime) {
				t.Errorf("message %q does not name retry time %q", msg, tc.wantTime)
			}
		})
	}
}

// TestBacklogReviewSurfacesRateLimitBudget pins the success-path pacing fact: a
// fetch's GraphQL budget threads through to the rateLimit block so a caller can
// pace itself.
func TestBacklogReviewSurfacesRateLimitBudget(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	reset := fixedClock.Add(30 * time.Minute)
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{issue(1, daysAgo(10))},
		TotalOpen: 1,
		RateLimit: &github.RateLimit{Remaining: 4321, ResetAt: reset},
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.RateLimit == nil {
		t.Fatal("RateLimit = nil, want budget fact")
	}
	if facts.RateLimit.Remaining != 4321 {
		t.Errorf("Remaining = %d, want 4321", facts.RateLimit.Remaining)
	}
	if !facts.RateLimit.ResetAt.Equal(reset) {
		t.Errorf("ResetAt = %v, want %v", facts.RateLimit.ResetAt, reset)
	}
}

// TestBacklogReviewOmitsRateLimitWhenAbsent pins that an unknown budget is
// omitted from the output, not rendered as a present-but-zero block. The
// assertion is on the marshaled bytes: after decode, a null pointer and an
// omitted field are indistinguishable, so only the raw JSON proves omitempty.
func TestBacklogReviewOmitsRateLimitWhenAbsent(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{issue(1, daysAgo(10))},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if strings.Contains(string(raw), "rateLimit") {
		t.Errorf("output names rateLimit when budget is absent: %s", raw)
	}
}

// TestBacklogReviewRejectsSplitManifestKey pins the misconfiguration path: when a
// repo's key is defined in more than one discovered manifest file, the tool fails
// loud rather than silently dropping an entry. The caller channel names only the
// repo — the contributing file paths stay on the server's stderr log.
func TestBacklogReviewRejectsSplitManifestKey(t *testing.T) {
	root := writeManifestFiles(t, map[string]string{
		"a-repos.yml": "acme/widgets:\n  staleness:\n    thresholdDays: 10\n",
		"b-repos.yml": "acme/widgets:\n  staleness:\n    thresholdDays: 20\n",
	})
	srv := New(WithFetcher(fakeFetcher{}), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})

	if !res.IsError {
		t.Fatalf("IsError = false, want true for a key split across files")
	}
	msg := contentText(res)
	if !strings.Contains(msg, "acme/widgets") {
		t.Errorf("error message %q does not name the repo acme/widgets", msg)
	}
	// The contributing file paths must stay on the server's stderr log, never the
	// caller channel — assert the leak doesn't regress.
	if strings.Contains(msg, "a-repos.yml") || strings.Contains(msg, "b-repos.yml") {
		t.Errorf("error message %q leaks manifest file paths to the caller", msg)
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

	if facts.Staleness.OpenIssueCount != 500 {
		t.Errorf("OpenIssueCount = %d, want 500 (exact)", facts.Staleness.OpenIssueCount)
	}
	if !facts.Staleness.FetchTruncated {
		t.Errorf("FetchTruncated = false, want true (fetched 3 of 500)")
	}
	if !facts.Staleness.ListTruncated {
		t.Errorf("ListTruncated = false, want true (3 stale, limit 2)")
	}
	if len(facts.Staleness.StaleIssues) != 2 {
		t.Errorf("listed %d stale issues, want 2 (the limit)", len(facts.Staleness.StaleIssues))
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

	if facts.Staleness.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (schema default)", facts.Staleness.Limit)
	}
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/backlog"
	"github.com/jakewan/overstory/internal/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeFetcher is a static Fetcher: the seam that lets the acceptance test
// exercise the tools end-to-end without invoking gh or the network. The
// activity* fields drive the trajectory reduction's second fetch; their zero
// value (empty result, nil error) makes that fetch a no-op success, so tests
// that don't care about trajectory are unaffected. The milestone* fields play
// the same role for the project_summary milestone fetch.
type fakeFetcher struct {
	result           github.IssueListResult
	err              error
	activityResult   github.IssueActivityResult
	activityErr      error
	prActivityResult github.PullRequestActivityResult
	prActivityErr    error
	milestones       github.MilestoneListResult
	milestonesErr    error
	pullRequests     github.PullRequestListResult
	pullRequestErr   error
	authoredResult   github.AuthoredActivityResult
	authoredErr      error
	// authoredByRepo drives the batch fan-out: AuthoredActivity returns the canned
	// outcome keyed by owner/repo when this is non-nil, so each repo in a batch can
	// take a different path (counts, not-found, throttle). When nil, the single
	// authoredResult/authoredErr fields apply — keeping every single-repo test green.
	authoredByRepo map[string]authoredCanned
	// authoredCalls counts AuthoredActivity invocations across the fan-out's
	// goroutines, so a backpressure test can assert "exactly one fetch ran." It is a
	// pointer because fakeFetcher is copied by value into the interface and on every
	// value-receiver call — a value atomic would unshare the count (and trip
	// copylocks). Nil leaves the count untracked.
	authoredCalls *atomic.Int64
	// authoredSeq, when non-nil, drives the outcome by call ordinal (1-based) rather
	// than by repo, so a test can pin behavior that depends on fetch order without
	// depending on which repo wins a concurrency slot (e.g. "first call throttles, a
	// later call would author-fail"). Requires authoredCalls to be set.
	authoredSeq func(call int64) authoredCanned
	// eventsResult/eventsErr drive the single-repo maintenance fetch; their zero
	// value (empty result, nil error) makes ListIssueEvents a no-op success so tests
	// that don't exercise maintenance are unaffected. eventsByRepo drives the batch
	// fan-out keyed by owner/repo (each repo can take a different path), and
	// eventsCalls counts invocations across the fan-out's goroutines for a
	// backpressure assertion — a pointer for the same copy-by-value reason as
	// authoredCalls.
	eventsResult github.IssueEventsResult
	eventsErr    error
	eventsByRepo map[string]eventsCanned
	eventsCalls  *atomic.Int64
	// Secondary-fetch call counters for projection's fetch-skip tests. Each is a
	// pointer for the same copy-by-value reason as authoredCalls: fakeFetcher is
	// held in the interface by value and copied on every value-receiver call, so a
	// plain counter would be mutated on a copy and stay invisible. Nil = untracked.
	activityCalls    *atomic.Int64
	prActivityCalls  *atomic.Int64
	milestoneCalls   *atomic.Int64
	pullRequestCalls *atomic.Int64
}

// eventsCanned is one repo's canned ListIssueEvents outcome for the batch fake.
// When block is set the call honors the context — it blocks until ctx is done and
// returns ctx.Err() — so a per-repo timeout test can drive a hung fetch.
type eventsCanned struct {
	result github.IssueEventsResult
	err    error
	block  bool
}

// authoredCanned is one repo's canned AuthoredActivity outcome for the batch fake.
// When block is set the call honors the context — it blocks until ctx is done and
// returns ctx.Err() — so a per-repo timeout test can drive a hung fetch.
type authoredCanned struct {
	result github.AuthoredActivityResult
	err    error
	block  bool
}

// withBudget stamps a RateLimit budget onto a result, for batch aggregation tests.
func withBudget(r github.AuthoredActivityResult, remaining int, reset time.Time) github.AuthoredActivityResult {
	r.RateLimit = &github.RateLimit{Remaining: remaining, ResetAt: reset}
	return r
}

func (f fakeFetcher) ListOpenIssues(_ context.Context, _ string, _ int) (github.IssueListResult, error) {
	return f.result, f.err
}

func (f fakeFetcher) ListIssuesUpdatedSince(_ context.Context, _ string, _ time.Time, _ int) (github.IssueActivityResult, error) {
	if f.activityCalls != nil {
		f.activityCalls.Add(1)
	}
	return f.activityResult, f.activityErr
}

func (f fakeFetcher) ListPullRequestsUpdatedSince(_ context.Context, _ string, _ time.Time, _ int) (github.PullRequestActivityResult, error) {
	if f.prActivityCalls != nil {
		f.prActivityCalls.Add(1)
	}
	return f.prActivityResult, f.prActivityErr
}

func (f fakeFetcher) ListOpenMilestones(_ context.Context, _ string, _ int) (github.MilestoneListResult, error) {
	if f.milestoneCalls != nil {
		f.milestoneCalls.Add(1)
	}
	return f.milestones, f.milestonesErr
}

func (f fakeFetcher) ListOpenPullRequests(_ context.Context, _ string, _ int) (github.PullRequestListResult, error) {
	if f.pullRequestCalls != nil {
		f.pullRequestCalls.Add(1)
	}
	return f.pullRequests, f.pullRequestErr
}

func (f fakeFetcher) AuthoredActivity(ctx context.Context, ownerRepo, _ string, _, _ time.Time) (github.AuthoredActivityResult, error) {
	// authoredSeq is keyed on the call ordinal, which only advances when authoredCalls
	// is set — without it every call would see ordinal 0 and silently misbehave. Fail
	// loudly on that setup error rather than producing misleading results.
	if f.authoredSeq != nil && f.authoredCalls == nil {
		panic("fakeFetcher: authoredSeq requires authoredCalls to be set")
	}
	var call int64
	if f.authoredCalls != nil {
		call = f.authoredCalls.Add(1)
	}
	if f.authoredSeq != nil {
		return resolveCanned(ctx, f.authoredSeq(call))
	}
	if f.authoredByRepo != nil {
		// A missing key is a test-setup omission, not a real fetch: surface it as an
		// error so a forgotten repo fails the test loudly rather than masquerading as
		// a successful zero-count result.
		c, ok := f.authoredByRepo[ownerRepo]
		if !ok {
			return github.AuthoredActivityResult{}, fmt.Errorf("fakeFetcher: no canned authored result for %q", ownerRepo)
		}
		return resolveCanned(ctx, c)
	}
	return f.authoredResult, f.authoredErr
}

// resolveCanned returns a canned outcome, honoring block by waiting on the context
// (so a per-repo timeout derived from it fires and yields ctx.Err()).
func resolveCanned(ctx context.Context, c authoredCanned) (github.AuthoredActivityResult, error) {
	if c.block {
		<-ctx.Done()
		return github.AuthoredActivityResult{}, ctx.Err()
	}
	return c.result, c.err
}

func (f fakeFetcher) ListIssueEvents(ctx context.Context, ownerRepo string, _ time.Time, _ int) (github.IssueEventsResult, error) {
	if f.eventsCalls != nil {
		f.eventsCalls.Add(1)
	}
	if f.eventsByRepo != nil {
		// A missing key is a test-setup omission, not a real fetch: surface it as an
		// error so a forgotten repo fails the test loudly rather than masquerading as
		// a successful empty-events result.
		c, ok := f.eventsByRepo[ownerRepo]
		if !ok {
			return github.IssueEventsResult{}, fmt.Errorf("fakeFetcher: no canned events for %q", ownerRepo)
		}
		if c.block {
			<-ctx.Done()
			return github.IssueEventsResult{}, ctx.Err()
		}
		return c.result, c.err
	}
	return f.eventsResult, f.eventsErr
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

// titledIssue builds an issue with a specific title for the overlap reduction,
// which keys off the title; activity and labels are irrelevant here.
func titledIssue(num int, title string) github.Issue {
	is := issue(num, daysAgo(1))
	is.Title = title
	return is
}

// crossLinkedIssue builds an issue for the cross-reference reduction.
// referencedBy is the set of issue numbers that cross-reference this one (the
// incoming edges GitHub records on this issue's timeline), so a directed edge
// runs from each referencer to num.
func crossLinkedIssue(num int, title string, referencedBy ...int) github.Issue {
	is := issue(num, daysAgo(1))
	is.Title = title
	is.ReferencedBy = referencedBy
	return is
}

// TestBacklogReviewSurfacesCriticalPath pins the critical-path / gate block on the
// grooming read: the same reduction project_summary surfaces, over the corpus
// backlog_review already fetches. A declared stream with an open critical-path
// member blocks its gate; one with none is cleared.
func TestBacklogReviewSurfacesCriticalPath(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n  criticalPath:\n    streams: [simulation, narrative, ui]\n    label: critical-path\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			labeledIssue(1, "area/simulation", "critical-path"), // simulation member, blocks its gate
			labeledIssue(2, "area/narrative", "critical-path"),  // narrative member
			labeledIssue(3, "area/simulation"),                  // not critical-path → not a member
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	cp := facts.CriticalPath

	if !cp.Configured {
		t.Fatalf("CriticalPath.Configured = false, want true")
	}
	wantOrder := []string{"simulation", "narrative", "ui"}
	for i, w := range wantOrder {
		if cp.Streams[i].Stream != w {
			t.Fatalf("stream[%d] = %q, want %q (declared order)", i, cp.Streams[i].Stream, w)
		}
	}
	if sim := cp.Streams[0]; sim.GateCleared || len(sim.Members) != 1 || sim.Members[0].Number != 1 {
		t.Errorf("simulation = %+v, want uncleared with member #1", sim)
	}
	if ui := cp.Streams[2]; !ui.GateCleared || len(ui.Members) != 0 {
		t.Errorf("ui = %+v, want cleared with no members", ui)
	}
}

// TestBacklogReviewSurfacesDependencyStructureWithoutDeferredConvention pins the
// #87 fix: a repo whose manifest declares no deferred convention still gets its
// authoritative native dependency structure. Before this block, the deferred
// reduction was the only projection of native edges on backlog_review, so a
// deferred-less repo saw none. The capstone issue now surfaces as blocked with
// its blocked-by edges (the direction the mention graph inverts), and its
// blockers surface as gates.
func TestBacklogReviewSurfacesDependencyStructureWithoutDeferredConvention(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")

	capstone := issue(7, daysAgo(1))
	capstone.BlockedBy = []github.DependencyRef{
		{Number: 42, Open: true}, {Number: 43, Open: true}, {Number: 44, Open: true},
		{Number: 45, Open: true}, {Number: 46, Open: true},
	}
	issues := []github.Issue{capstone}
	for _, n := range []int{42, 43, 44, 45, 46} {
		b := issue(n, daysAgo(1))
		b.Blocking = []github.DependencyRef{{Number: 7, Open: true}}
		issues = append(issues, b)
	}
	fetcher := fakeFetcher{result: github.IssueListResult{Issues: issues, TotalOpen: 6}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	// The deferred block is a no-op here — proof the repo has no deferred convention.
	if facts.Deferred.Configured {
		t.Fatalf("Deferred.Configured = true, want false (no deferred convention)")
	}
	dep := facts.Dependencies
	if dep == nil {
		t.Fatal("Dependencies block absent; want present on the full composite")
	}
	if dep.BlockedCount != 1 || dep.ReadyCount != 5 {
		t.Errorf("counts blocked=%d ready=%d, want 1/5", dep.BlockedCount, dep.ReadyCount)
	}
	if len(dep.Blocked) != 1 || dep.Blocked[0].Number != 7 {
		t.Fatalf("Blocked = %+v, want [#7]", dep.Blocked)
	}
	if got := dep.Blocked[0].BlockedBy; len(got) != 5 || got[0] != 42 || got[4] != 46 {
		t.Errorf("#7 BlockedBy = %v, want [42..46] (authoritative direction)", got)
	}
	if len(dep.Gates) != 5 {
		t.Errorf("Gates = %d, want 5 (the blockers)", len(dep.Gates))
	}
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

// TestBacklogReviewSurfacesTitleOverlap pins the overlap grooming signal: given a
// manifest threshold, the tool groups open issues with similar titles as candidate
// duplicates, surfaces the shared words as evidence, and leaves unrelated issues
// out — alongside the other reduction blocks. The cluster titles normalize
// identically so the test pins wiring and grouping, not a fragile trigram score
// (score-boundary behavior is covered in the backlog package's unit tests).
func TestBacklogReviewSurfacesTitleOverlap(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  overlap:\n    titleSimilarityThreshold: 0.5\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			titledIssue(1, "Fix login bug"),
			titledIssue(2, "Fix login bug!"), // normalizes identically → grouped
			titledIssue(3, "Add dark mode"),  // unrelated
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	ov := facts.Overlap

	if ov.TitleThreshold != 0.5 {
		t.Errorf("TitleThreshold = %g, want 0.5", ov.TitleThreshold)
	}
	if ov.GroupCount != 1 {
		t.Fatalf("GroupCount = %d, want 1; groups=%+v", ov.GroupCount, ov.Groups)
	}
	g := ov.Groups[0]
	if len(g.Issues) != 2 || g.Issues[0].Number != 1 || g.Issues[1].Number != 2 {
		t.Errorf("group members = %+v, want issues 1 and 2", g.Issues)
	}
	want := []string{"bug", "fix", "login"}
	if len(g.SharedTokens) != len(want) {
		t.Fatalf("SharedTokens = %v, want %v", g.SharedTokens, want)
	}
	for i, w := range want {
		if g.SharedTokens[i] != w {
			t.Errorf("SharedTokens[%d] = %q, want %q", i, g.SharedTokens[i], w)
		}
	}
	if ov.LargestGroupSize != 2 {
		t.Errorf("LargestGroupSize = %d, want 2", ov.LargestGroupSize)
	}
	// The other blocks still reduce the same window.
	if facts.Staleness.OpenIssueCount != 3 {
		t.Errorf("Staleness.OpenIssueCount = %d, want 3", facts.Staleness.OpenIssueCount)
	}
}

// TestBacklogReviewOverlapGenericDefault pins the out-of-box behavior: a repo with
// no overlap block still groups similar titles via the generic default threshold.
func TestBacklogReviewOverlapGenericDefault(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{titledIssue(1, "Cache timeout error"), titledIssue(2, "Cache timeout error")},
		TotalOpen: 2,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.Overlap.TitleThreshold != 0.5 {
		t.Errorf("TitleThreshold = %g, want 0.5 (generic default)", facts.Overlap.TitleThreshold)
	}
	if facts.Overlap.GroupCount != 1 {
		t.Errorf("GroupCount = %d, want 1", facts.Overlap.GroupCount)
	}
}

// TestBacklogReviewSurfacesCrossReferences pins the cross-reference grooming
// signal: given open issues that reference one another, the tool groups them as a
// candidate-consolidation cluster, surfaces the directed reference edges as
// evidence, and leaves unreferenced issues out — alongside the other blocks.
func TestBacklogReviewSurfacesCrossReferences(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			crossLinkedIssue(1, "Tracking issue", 2), // #1 is referenced by #2
			crossLinkedIssue(2, "Sub-task A"),
			crossLinkedIssue(3, "Unrelated"),
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	cr := facts.CrossRef

	if cr.GroupCount != 1 {
		t.Fatalf("GroupCount = %d, want 1; groups=%+v", cr.GroupCount, cr.Groups)
	}
	g := cr.Groups[0]
	if len(g.Issues) != 2 || g.Issues[0].Number != 1 || g.Issues[1].Number != 2 {
		t.Errorf("group members = %+v, want issues 1 and 2", g.Issues)
	}
	if len(g.References) != 1 || g.References[0].From != 2 || g.References[0].To != 1 {
		t.Errorf("References = %+v, want one edge 2->1", g.References)
	}
	if cr.LargestGroupSize != 2 {
		t.Errorf("LargestGroupSize = %d, want 2", cr.LargestGroupSize)
	}
	// The other blocks still reduce the same window.
	if facts.Staleness.OpenIssueCount != 3 {
		t.Errorf("Staleness.OpenIssueCount = %d, want 3", facts.Staleness.OpenIssueCount)
	}
}

// TestBacklogReviewCrossRefRunsWithoutManifestEntry pins that the cross-reference
// reduction runs unconditionally: a repo with no manifest entry at all still gets
// the block (there is no per-repo knob to enable).
func TestBacklogReviewCrossRefRunsWithoutManifestEntry(t *testing.T) {
	root := writeManifestDir(t, "other/repo:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{crossLinkedIssue(1, "A", 2), crossLinkedIssue(2, "B")},
		TotalOpen: 2,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.CrossRef.GroupCount != 1 {
		t.Errorf("GroupCount = %d, want 1 (runs unconditionally)", facts.CrossRef.GroupCount)
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
	// Both inactive issues (1 and 3) carry a deferred label, so staleness — which
	// now means "neglected" — excludes them rather than double-counting parked
	// work as stale. Only the active, non-deferred issue 2 remains, and it is
	// fresh, so nothing is stale.
	if facts.Staleness.StaleCount != 0 {
		t.Errorf("Staleness.StaleCount = %d, want 0 (both stale issues are deferred)", facts.Staleness.StaleCount)
	}
	if facts.Staleness.DeferredExcludedCount != 2 {
		t.Errorf("Staleness.DeferredExcludedCount = %d, want 2", facts.Staleness.DeferredExcludedCount)
	}
}

// TestBacklogReviewDeferredSurfacesBodyRefs pins the dependency-readiness signal
// end-to-end (#32): each deferred issue carries the distinct #N references parsed
// from its (plaintext) body, with PR references and the issue's own number
// excluded, so a client can tell whether a parked issue's blocker has closed.
func TestBacklogReviewDeferredSurfacesBodyRefs(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	withRefs := deferredIssue(1, daysAgo(100), "deferred")
	withRefs.BodyText = "Blocked by #10 and #11. Also #10. Self #1. Needs PR #99 first."
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{withRefs},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	got := facts.Deferred.DeferredIssues[0].BodyRefs
	if len(got) != 2 || got[0] != 10 || got[1] != 11 {
		t.Errorf("BodyRefs = %v, want [10 11] (deduped, sorted, PR + self excluded)", got)
	}
}

// TestBacklogReviewDeferredBodyRefsEmptySerializesAsArray pins the non-nil
// convention through the JSON round-trip: a deferred issue with no body
// references must serialize bodyRefs as [], not null, so a client never sees a
// null it has to special-case.
func TestBacklogReviewDeferredBodyRefsEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	noRefs := deferredIssue(1, daysAgo(100), "deferred")
	noRefs.BodyText = "Parked for now; no dependencies."
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noRefs},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	// After a JSON round-trip, `[]` decodes to a non-nil empty slice and `null` to
	// nil — so a non-nil slice here proves the encoder emitted [].
	if facts.Deferred.DeferredIssues[0].BodyRefs == nil {
		t.Error("BodyRefs = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

// TestBacklogReviewDeferredSurfacesNativeBlockedBy pins the authoritative
// dependency signal (#60): each deferred issue carries the OPEN native blocked-by
// edge numbers — ascending, with closed blockers omitted (they no longer block) —
// so a client can trust whether a parked issue is actually blocked, distinct from
// the heuristic bodyRefs. A window-truncated edge set sets blockedByTruncated so a
// caller knows absence past the window is not proof of readiness.
func TestBacklogReviewDeferredSurfacesNativeBlockedBy(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	blocked := deferredIssue(1, daysAgo(100), "deferred")
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

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	di := facts.Deferred.DeferredIssues[0]
	if len(di.BlockedBy) != 2 || di.BlockedBy[0] != 7 || di.BlockedBy[1] != 11 {
		t.Errorf("BlockedBy = %v, want [7 11] (open only, ascending)", di.BlockedBy)
	}
	if !di.BlockedByTruncated {
		t.Error("BlockedByTruncated = false, want true (edge set exceeded the fetch window)")
	}
}

// TestBacklogReviewDeferredBlockedByEmptySerializesAsArray pins the non-nil
// convention through the JSON round-trip: a deferred issue with no native blockers
// must serialize blockedBy as [], not null, mirroring bodyRefs.
func TestBacklogReviewDeferredBlockedByEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	noBlockers := deferredIssue(1, daysAgo(100), "deferred")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noBlockers},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	if facts.Deferred.DeferredIssues[0].BlockedBy == nil {
		t.Error("BlockedBy = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

// TestBacklogReviewDeferredSurfacesNativeBlocking pins the reverse-direction
// authoritative signal (#60): each deferred issue carries the OPEN native blocking
// edge numbers — ascending, with closed downstream issues omitted (a closed issue is
// no longer gated) — so a maintainer sees how much still-open downstream work a
// parked issue stands in front of, distinct from blockedBy (what gates the parked
// issue itself). A window-truncated edge set sets blockingTruncated.
func TestBacklogReviewDeferredSurfacesNativeBlocking(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	blocked := deferredIssue(1, daysAgo(100), "deferred")
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

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	di := facts.Deferred.DeferredIssues[0]
	if len(di.Blocking) != 2 || di.Blocking[0] != 17 || di.Blocking[1] != 21 {
		t.Errorf("Blocking = %v, want [17 21] (open only, ascending)", di.Blocking)
	}
	if !di.BlockingTruncated {
		t.Error("BlockingTruncated = false, want true (edge set exceeded the fetch window)")
	}
}

// TestBacklogReviewDeferredBlockingEmptySerializesAsArray pins the non-nil
// convention through the JSON round-trip: a deferred issue gating nothing must
// serialize blocking as [], not null, mirroring blockedBy.
func TestBacklogReviewDeferredBlockingEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	noBlocking := deferredIssue(1, daysAgo(100), "deferred")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noBlocking},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	if facts.Deferred.DeferredIssues[0].Blocking == nil {
		t.Error("Blocking = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

func TestBacklogReviewDeferredSurfacesNativeSubIssues(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	parent := deferredIssue(1, daysAgo(100), "deferred")
	parent.SubIssues = []github.DependencyRef{
		{Number: 27, Open: true},
		{Number: 23, Open: true},
		{Number: 25, Open: false}, // completed child no longer gates — excluded
	}
	parent.SubIssuesTruncated = true
	// The summary is the untruncated authoritative pair, counted over all children
	// (cross-repo and beyond-window alike), so total minus completed can exceed the
	// listed open count — here 6-3=3 against 2 listed.
	parent.SubIssuesTotal = 6
	parent.SubIssuesCompleted = 3
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{parent},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	di := facts.Deferred.DeferredIssues[0]
	if len(di.SubIssues) != 2 || di.SubIssues[0] != 23 || di.SubIssues[1] != 27 {
		t.Errorf("SubIssues = %v, want [23 27] (open only, ascending)", di.SubIssues)
	}
	if !di.SubIssuesTruncated {
		t.Error("SubIssuesTruncated = false, want true (child set exceeded the fetch window)")
	}
	if di.SubIssuesTotal != 6 || di.SubIssuesCompleted != 3 {
		t.Errorf("completion = %d/%d, want 3/6 (completed/total)", di.SubIssuesCompleted, di.SubIssuesTotal)
	}
}

// TestBacklogReviewDeferredSubIssuesEmptySerializesAsArray pins the non-nil
// convention through the JSON round-trip: a deferred issue with no children must
// serialize subIssues as [], not null, mirroring blockedBy/blocking.
func TestBacklogReviewDeferredSubIssuesEmptySerializesAsArray(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	noChildren := deferredIssue(1, daysAgo(100), "deferred")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{noChildren},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Deferred.DeferredIssues) != 1 {
		t.Fatalf("listed %d deferred issues, want 1", len(facts.Deferred.DeferredIssues))
	}
	if facts.Deferred.DeferredIssues[0].SubIssues == nil {
		t.Error("SubIssues = nil (serialized as null), want non-nil empty slice (serialized as [])")
	}
}

// TestBacklogReviewSurfacesOpenIssueSet pins the resolvable open-issue set on the
// grooming read: the fetched open numbers (the surface a caller resolves a deferred
// issue's bodyRefs against), ascending and complete on a non-truncated fetch.
func TestBacklogReviewSurfacesOpenIssueSet(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{issue(3, daysAgo(10)), issue(1, daysAgo(10)), issue(2, daysAgo(10))},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if got := facts.OpenIssueSet.Numbers; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("OpenIssueSet.Numbers = %v, want [1 2 3] (ascending)", got)
	}
	if facts.OpenIssueSet.FetchTruncated {
		t.Error("OpenIssueSet.FetchTruncated = true, want false (whole window fetched)")
	}
}

// TestBacklogReviewOpenIssueSetUncappedByLimit is the soundness guard for the
// grooming read: the open-issue set is the full fetched window, never capped by
// limit. An open issue beyond the deferred list cap must still appear in numbers,
// or a real open blocker would read as ∉ set.
func TestBacklogReviewOpenIssueSetUncappedByLimit(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			deferredIssue(1, daysAgo(100), "deferred"),
			deferredIssue(2, daysAgo(100), "deferred"),
			deferredIssue(3, daysAgo(100), "deferred"), // #3 sits beyond the limit-2 deferred list cap
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 2}))
	// The deferred list IS capped at the limit — proving the set is not derived from it.
	if !facts.Deferred.ListTruncated || len(facts.Deferred.DeferredIssues) != 2 {
		t.Fatalf("deferred list not capped at 2 (got %d, truncated=%v) — test no longer guards the cap",
			len(facts.Deferred.DeferredIssues), facts.Deferred.ListTruncated)
	}
	want := []int{1, 2, 3}
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

// TestBacklogReviewOpenIssueSetTruncated pins the truncation seam on the grooming
// read: a window that didn't cover every open issue marks numbers as a floor.
func TestBacklogReviewOpenIssueSetTruncated(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues:    []github.Issue{issue(1, daysAgo(10))},
		TotalOpen: 10,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if !facts.OpenIssueSet.FetchTruncated {
		t.Error("OpenIssueSet.FetchTruncated = false, want true (window did not cover every open issue)")
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
	// Staleness still works in the absence of a deferred convention, and with no
	// deferred labels nothing is excluded — behavior is identical to before.
	if facts.Staleness.StaleCount != 1 {
		t.Errorf("Staleness.StaleCount = %d, want 1", facts.Staleness.StaleCount)
	}
	if facts.Staleness.DeferredExcludedCount != 0 {
		t.Errorf("Staleness.DeferredExcludedCount = %d, want 0 (no deferred convention)", facts.Staleness.DeferredExcludedCount)
	}
}

// TestBacklogReviewStalenessExcludesDeferred pins the core of #28: a deferred
// issue is a third state, neither stale nor fresh. The fixture populates all
// four partition cells — deferred-and-stale, deferred-and-fresh, plain-stale,
// plain-fresh — and the three counts must partition the fetched window exactly.
func TestBacklogReviewStalenessExcludesDeferred(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			deferredIssue(1, daysAgo(100), "deferred"), // deferred + inactive → excluded
			deferredIssue(2, daysAgo(50)),              // plain stale
			deferredIssue(3, daysAgo(5), "deferred"),   // deferred + active → excluded
			deferredIssue(4, daysAgo(5)),               // plain fresh
		},
		TotalOpen: 4,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	s := facts.Staleness

	if s.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1 (only the non-deferred inactive issue)", s.StaleCount)
	}
	if s.FreshCount != 1 {
		t.Errorf("FreshCount = %d, want 1 (only the non-deferred active issue)", s.FreshCount)
	}
	if s.DeferredExcludedCount != 2 {
		t.Errorf("DeferredExcludedCount = %d, want 2", s.DeferredExcludedCount)
	}
	if got := s.StaleCount + s.FreshCount + s.DeferredExcludedCount; got != s.FetchedCount {
		t.Errorf("partition broken: stale+fresh+excluded = %d, want fetchedCount %d", got, s.FetchedCount)
	}
	// The excluded deferred issue must not leak into the listed stale issues.
	for _, si := range s.StaleIssues {
		if si.Number == 1 || si.Number == 3 {
			t.Errorf("deferred issue #%d leaked into staleIssues", si.Number)
		}
	}
	if len(s.StaleIssues) != 1 {
		t.Fatalf("len(StaleIssues) = %d, want 1", len(s.StaleIssues))
	}
	if s.StaleIssues[0].Number != 2 {
		t.Errorf("staleIssues[0] = #%d, want #2", s.StaleIssues[0].Number)
	}
}

// TestBacklogReviewStalenessExclusionUncappedByListLimit guards the capping
// trap: the exclusion set is derived from the full fetched window, never from the
// deferred block's list (which is capped at the limit). With a limit below the
// deferred count, every deferred issue must still leave the staleness universe.
func TestBacklogReviewStalenessExclusionUncappedByListLimit(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			deferredIssue(1, daysAgo(100), "deferred"),
			deferredIssue(2, daysAgo(100), "deferred"),
			deferredIssue(3, daysAgo(100), "deferred"),
		},
		TotalOpen: 3,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "limit": 2}))
	s := facts.Staleness

	if s.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0 (all three are deferred)", s.StaleCount)
	}
	if s.DeferredExcludedCount != 3 {
		t.Errorf("DeferredExcludedCount = %d, want 3 (uncapped by the list limit)", s.DeferredExcludedCount)
	}
	// The deferred block's list is capped at the limit, proving the exclusion set
	// is not derived from it.
	if !facts.Deferred.ListTruncated || len(facts.Deferred.DeferredIssues) != 2 {
		t.Errorf("expected the deferred list capped at 2; got %d (truncated=%v)",
			len(facts.Deferred.DeferredIssues), facts.Deferred.ListTruncated)
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

// activityIssue builds an issue-activity record for the trajectory reduction's
// second fetch. closedDaysAgo < 0 means still open (zero ClosedAt); otherwise the
// issue closed that many days before the fixed clock.
func activityIssue(num, createdDaysAgo, closedDaysAgo int) github.IssueActivity {
	a := github.IssueActivity{Number: num, CreatedAt: daysAgo(createdDaysAgo)}
	if closedDaysAgo >= 0 {
		a.ClosedAt = daysAgo(closedDaysAgo)
	}
	return a
}

// prActivity builds a pull-request-activity record for the PR-trajectory
// reduction's fetch. closedDaysAgo < 0 means still open (zero ClosedAt); otherwise
// the PR closed (merged or closed-without-merge) that many days before the fixed
// clock.
func prActivity(num, openedDaysAgo, closedDaysAgo int) github.PullRequestActivity {
	a := github.PullRequestActivity{Number: num, CreatedAt: daysAgo(openedDaysAgo)}
	if closedDaysAgo >= 0 {
		a.ClosedAt = daysAgo(closedDaysAgo)
	}
	return a
}

// TestBacklogReviewSurfacesPRTrajectory pins the change-request closure-ratio
// grooming signal: given a manifest declaring lookback windows, the tool counts
// pull requests opened and closed within each cumulative window and reports the
// net — over a dedicated OPEN+CLOSED+MERGED PR fetch reusing the trajectory
// windows, alongside the issue trajectory and the open-window blocks.
func TestBacklogReviewSurfacesPRTrajectory(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  trajectory:\n    windows: [7, 30, 90]\n")
	fetcher := fakeFetcher{
		result: github.IssueListResult{
			Issues:    []github.Issue{issue(1, daysAgo(10))},
			TotalOpen: 1,
		},
		prActivityResult: github.PullRequestActivityResult{Activities: []github.PullRequestActivity{
			prActivity(1, 2, -1),  // opened 2d, open   → opened in 7/30/90
			prActivity(2, 5, -1),  // opened 5d, open   → opened in 7/30/90
			prActivity(3, 20, -1), // opened 20d, open  → opened in 30/90
			prActivity(4, 80, 3),  // opened 80d, closed 3d → closed in 7/30/90; opened in 90
			prActivity(5, 60, 40), // opened 60d, closed 40d → closed in 90; opened in 90
			prActivity(6, 1, 1),   // opened 1d, closed 1d → opened+closed in all
		}},
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	pt := facts.PRTrajectory

	if !pt.Available {
		t.Fatalf("PRTrajectory.Available = false, want true; %+v", pt)
	}
	if pt.FetchedCount != 6 {
		t.Errorf("FetchedCount = %d, want 6", pt.FetchedCount)
	}
	want := []backlog.PRTrajectoryWindow{
		{Days: 7, Opened: 3, Closed: 2, Net: 1},
		{Days: 30, Opened: 4, Closed: 2, Net: 2},
		{Days: 90, Opened: 6, Closed: 3, Net: 3},
	}
	if len(pt.Windows) != len(want) {
		t.Fatalf("Windows = %+v, want %+v", pt.Windows, want)
	}
	for i, w := range want {
		if pt.Windows[i] != w {
			t.Errorf("Windows[%d] = %+v, want %+v", i, pt.Windows[i], w)
		}
	}
	// The issue trajectory and the open-window blocks are unaffected by the PR fetch.
	if facts.Staleness.OpenIssueCount != 1 {
		t.Errorf("Staleness.OpenIssueCount = %d, want 1 (the fetches coexist)", facts.Staleness.OpenIssueCount)
	}
}

// TestBacklogReviewPRTrajectoryFetchTruncated pins the never-silently-truncate
// contract for the aggregate PR block: when the PR-activity fetch hit its cap, the
// counts are a lower bound and FetchTruncated says so.
func TestBacklogReviewPRTrajectoryFetchTruncated(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{
		result: github.IssueListResult{Issues: []github.Issue{issue(1, daysAgo(10))}, TotalOpen: 1},
		prActivityResult: github.PullRequestActivityResult{
			Activities: []github.PullRequestActivity{prActivity(1, 2, -1)},
			Truncated:  true,
		},
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if !facts.PRTrajectory.FetchTruncated {
		t.Error("PRTrajectory.FetchTruncated = false, want true")
	}
}

// TestBacklogReviewPRTrajectoryDegradesOnFetchError pins the degrade design: a
// failed PR-activity fetch never fails the whole call — the other blocks still
// return and the PR-trajectory block is marked unavailable with a distinguishable
// reason. A throttle additionally surfaces its recovery signal as the rate-limit
// budget, winning over the healthy open-fetch budget so a self-pacing caller
// learns it was throttled.
func TestBacklogReviewPRTrajectoryDegradesOnFetchError(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	reset := fixedClock.Add(15 * time.Minute)
	for _, tc := range []struct {
		name       string
		err        error
		wantReason string
	}{
		{"rate limited", github.RateLimitedError{ResetAt: reset}, "rate_limited"},
		{"generic failure", github.ErrRepoNotFound, "fetch_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := fakeFetcher{
				result: github.IssueListResult{
					Issues:    []github.Issue{issue(1, daysAgo(100))},
					TotalOpen: 1,
					// A healthy open-fetch budget that must NOT be reported on a throttle-degrade.
					RateLimit: &github.RateLimit{Remaining: 5000, ResetAt: fixedClock.Add(time.Hour)},
				},
				prActivityErr: tc.err,
			}
			srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

			res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})
			if res.IsError {
				t.Fatalf("IsError = true, want false (degrade, not fail): %s", contentText(res))
			}
			facts := decodeFacts(t, res)
			if facts.PRTrajectory.Available {
				t.Error("PRTrajectory.Available = true, want false (fetch failed)")
			}
			if facts.PRTrajectory.Unavailable != tc.wantReason {
				t.Errorf("PRTrajectory.Unavailable = %q, want %q", facts.PRTrajectory.Unavailable, tc.wantReason)
			}
			// The other blocks still reduce the successful open fetch.
			if facts.Staleness.StaleCount != 1 {
				t.Errorf("Staleness.StaleCount = %d, want 1 (degrade preserves other blocks)", facts.Staleness.StaleCount)
			}
			if tc.wantReason == "rate_limited" {
				if facts.RateLimit == nil {
					t.Fatal("RateLimit = nil, want the throttle recovery signal")
				}
				if facts.RateLimit.Remaining != 0 {
					t.Errorf("RateLimit.Remaining = %d, want 0 (throttle wins over healthy open budget)", facts.RateLimit.Remaining)
				}
				if !facts.RateLimit.ResetAt.Equal(reset) {
					t.Errorf("RateLimit.ResetAt = %v, want %v (the throttle's reset)", facts.RateLimit.ResetAt, reset)
				}
			}
		})
	}
}

// TestBacklogReviewSurfacesTrajectory pins the creation-vs-closure grooming
// signal: given a manifest declaring lookback windows, the tool counts issues
// created and closed within each cumulative window and reports the net — over a
// second OPEN+CLOSED fetch, alongside the open-window blocks.
func TestBacklogReviewSurfacesTrajectory(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  trajectory:\n    windows: [7, 30, 90]\n")
	fetcher := fakeFetcher{
		result: github.IssueListResult{
			Issues:    []github.Issue{issue(1, daysAgo(10)), issue(2, daysAgo(5))},
			TotalOpen: 2,
		},
		activityResult: github.IssueActivityResult{Activities: []github.IssueActivity{
			activityIssue(1, 2, -1),  // created 2d, open  → created in 7/30/90
			activityIssue(2, 5, -1),  // created 5d, open  → created in 7/30/90
			activityIssue(3, 20, -1), // created 20d, open → created in 30/90
			activityIssue(4, 80, 3),  // created 80d, closed 3d → closed in 7/30/90; created in 90
			activityIssue(5, 60, 40), // created 60d, closed 40d → closed in 90; created in 90
			activityIssue(6, 1, 1),   // created 1d, closed 1d → created+closed in all
		}},
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	tr := facts.Trajectory

	if !tr.Available {
		t.Fatalf("Trajectory.Available = false, want true; %+v", tr)
	}
	if tr.FetchedCount != 6 {
		t.Errorf("FetchedCount = %d, want 6", tr.FetchedCount)
	}
	want := []backlog.TrajectoryWindow{
		{Days: 7, Created: 3, Closed: 2, Net: 1},
		{Days: 30, Created: 4, Closed: 2, Net: 2},
		{Days: 90, Created: 6, Closed: 3, Net: 3},
	}
	if len(tr.Windows) != len(want) {
		t.Fatalf("Windows = %+v, want %+v", tr.Windows, want)
	}
	for i, w := range want {
		if tr.Windows[i] != w {
			t.Errorf("Windows[%d] = %+v, want %+v", i, tr.Windows[i], w)
		}
	}
	// The open-window blocks still reduce the first fetch.
	if facts.Staleness.OpenIssueCount != 2 {
		t.Errorf("Staleness.OpenIssueCount = %d, want 2 (the two fetches coexist)", facts.Staleness.OpenIssueCount)
	}
}

// TestBacklogReviewTrajectoryGenericDefault pins the out-of-box behavior: a repo
// with no trajectory block still reports the default [7,30,90] windows.
func TestBacklogReviewTrajectoryGenericDefault(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{
		result:         github.IssueListResult{Issues: []github.Issue{issue(1, daysAgo(10))}, TotalOpen: 1},
		activityResult: github.IssueActivityResult{Activities: []github.IssueActivity{activityIssue(1, 2, -1)}},
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	days := make([]int, len(facts.Trajectory.Windows))
	for i, w := range facts.Trajectory.Windows {
		days[i] = w.Days
	}
	if len(days) != 3 || days[0] != 7 || days[1] != 30 || days[2] != 90 {
		t.Errorf("default windows = %v, want [7 30 90]", days)
	}
}

// TestBacklogReviewTrajectoryFetchTruncated pins the never-silently-truncate
// contract for the aggregate block: when the activity fetch hit its cap, the
// counts are a lower bound and FetchTruncated says so.
func TestBacklogReviewTrajectoryFetchTruncated(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	fetcher := fakeFetcher{
		result: github.IssueListResult{Issues: []github.Issue{issue(1, daysAgo(10))}, TotalOpen: 1},
		activityResult: github.IssueActivityResult{
			Activities: []github.IssueActivity{activityIssue(1, 2, -1)},
			Truncated:  true,
		},
	}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeFacts(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if !facts.Trajectory.FetchTruncated {
		t.Error("Trajectory.FetchTruncated = false, want true")
	}
}

// TestBacklogReviewTrajectoryDegradesOnFetchError pins the degrade design: a
// failed second fetch never fails the whole call — the other blocks still return
// and the trajectory block is marked unavailable with a distinguishable reason. A
// throttle additionally surfaces its recovery signal as the rate-limit budget,
// overriding the now-stale open-fetch budget rather than reporting "you have
// budget" at the moment the second fetch was throttled.
func TestBacklogReviewTrajectoryDegradesOnFetchError(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	reset := fixedClock.Add(15 * time.Minute)
	for _, tc := range []struct {
		name       string
		err        error
		wantReason string
	}{
		{"rate limited", github.RateLimitedError{ResetAt: reset}, "rate_limited"},
		{"generic failure", github.ErrRepoNotFound, "fetch_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := fakeFetcher{
				result: github.IssueListResult{
					Issues:    []github.Issue{issue(1, daysAgo(100))},
					TotalOpen: 1,
					// A healthy open-fetch budget that must NOT be reported on a throttle-degrade.
					RateLimit: &github.RateLimit{Remaining: 5000, ResetAt: fixedClock.Add(time.Hour)},
				},
				activityErr: tc.err,
			}
			srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

			res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})
			if res.IsError {
				t.Fatalf("IsError = true, want false (degrade, not fail): %s", contentText(res))
			}
			facts := decodeFacts(t, res)
			if facts.Trajectory.Available {
				t.Error("Trajectory.Available = true, want false (fetch failed)")
			}
			if facts.Trajectory.Unavailable != tc.wantReason {
				t.Errorf("Trajectory.Unavailable = %q, want %q", facts.Trajectory.Unavailable, tc.wantReason)
			}
			// The other blocks still reduce the successful open fetch.
			if facts.Staleness.StaleCount != 1 {
				t.Errorf("Staleness.StaleCount = %d, want 1 (degrade preserves other blocks)", facts.Staleness.StaleCount)
			}
			if tc.wantReason == "rate_limited" {
				if facts.RateLimit == nil {
					t.Fatal("RateLimit = nil, want the throttle recovery signal")
				}
				if facts.RateLimit.Remaining != 0 {
					t.Errorf("RateLimit.Remaining = %d, want 0 (throttle overrides stale open budget)", facts.RateLimit.Remaining)
				}
				if !facts.RateLimit.ResetAt.Equal(reset) {
					t.Errorf("RateLimit.ResetAt = %v, want %v (the throttle's reset)", facts.RateLimit.ResetAt, reset)
				}
			}
		})
	}
}

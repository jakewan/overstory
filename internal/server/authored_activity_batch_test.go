package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/authored"
	"github.com/jakewan/overstory/internal/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rawCallAuthoredActivityBatch drives the batch tool through the in-memory MCP
// session, returning both the transport error and the result so validation
// cases can assert failure regardless of whether it surfaces as a schema
// (transport-level) rejection or a handler-level IsError.
func rawCallAuthoredActivityBatch(t *testing.T, srv *mcp.Server, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	cs := connect(t, srv)
	return cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "authored_activity_batch",
		Arguments: args,
	})
}

// callAuthoredActivityBatch is the happy-path driver: a transport error fails
// the test, the result is returned for assertions.
func callAuthoredActivityBatch(t *testing.T, srv *mcp.Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := rawCallAuthoredActivityBatch(t, srv, args)
	if err != nil {
		t.Fatalf("call authored_activity_batch: %v", err)
	}
	return res
}

func decodeBatchFacts(t *testing.T, res *mcp.CallToolResult) authored.BatchFacts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts authored.BatchFacts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("unmarshal batch facts: %v", err)
	}
	return facts
}

// authoredBatchFetcher builds a fakeFetcher whose AuthoredActivity is keyed by
// owner/repo, so a fan-out can be driven to a different outcome per repo.
func authoredBatchFetcher(byRepo map[string]authoredCanned) fakeFetcher {
	return fakeFetcher{authoredByRepo: byRepo}
}

func sixCounts(commits, issuesOpened, prsOpened, reviews, prsEngaged, issuesEngaged int) github.AuthoredActivityResult {
	return github.AuthoredActivityResult{
		CommitsAuthored:     commits,
		IssuesOpened:        issuesOpened,
		PullRequestsOpened:  prsOpened,
		ReviewsSubmitted:    reviews,
		PullRequestsEngaged: prsEngaged,
		IssuesEngaged:       issuesEngaged,
	}
}

// TestAuthoredActivityBatchSurfacesPerRepoResults pins the core contract: a list
// of repos fans out to per-repo entries in request order, the successful repos
// carry counts with per-category fidelity labels, a failing repo degrades to a
// marker (not the whole batch), the window/author/identity are echoed once, and
// the aggregated rateLimit is the tightest budget across the successful repos.
func TestAuthoredActivityBatchSurfacesPerRepoResults(t *testing.T) {
	r1 := fixedClock.Add(time.Hour)
	r2 := fixedClock.Add(30 * time.Minute)
	fetcher := authoredBatchFetcher(map[string]authoredCanned{
		"acme/widgets":  {result: withBudget(sixCounts(12, 3, 5, 7, 9, 4), 4500, r1)},
		"acme/gadgets":  {result: withBudget(sixCounts(1, 1, 1, 1, 1, 1), 100, r2)}, // tighter budget
		"ghost/missing": {err: github.ErrRepoNotFound},
	})
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	facts := decodeBatchFacts(t, callAuthoredActivityBatch(t, srv, map[string]any{
		"repos":  []any{"acme/widgets", "acme/gadgets", "ghost/missing"},
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
		"until":  "2026-06-01T00:00:00Z",
	}))

	if facts.Author != "alice" {
		t.Errorf("Author = %q, want alice", facts.Author)
	}
	if !facts.GeneratedAt.Equal(fixedClock) {
		t.Errorf("GeneratedAt = %v, want %v", facts.GeneratedAt, fixedClock)
	}
	if len(facts.Repos) != 3 {
		t.Fatalf("len(Repos) = %d, want 3", len(facts.Repos))
	}
	// Request order is preserved.
	wantOrder := []string{"acme/widgets", "acme/gadgets", "ghost/missing"}
	for i, w := range wantOrder {
		if facts.Repos[i].Repo != w {
			t.Errorf("Repos[%d].Repo = %q, want %q", i, facts.Repos[i].Repo, w)
		}
	}
	// First repo: available, full counts, fidelity labels present.
	w0 := facts.Repos[0]
	if !w0.Available || w0.Counts == nil {
		t.Fatalf("Repos[0] = %+v, want available with counts", w0)
	}
	if w0.Counts.CommitsAuthored.Count != 12 {
		t.Errorf("widgets commitsAuthored = %d, want 12", w0.Counts.CommitsAuthored.Count)
	}
	if strings.TrimSpace(w0.Counts.IssuesOpened.Fidelity) == "" {
		t.Error("widgets issuesOpened carries no fidelity label")
	}
	// Failing repo: a marker, not counts, not a whole-batch error.
	w2 := facts.Repos[2]
	if w2.Available || w2.Counts != nil {
		t.Errorf("Repos[2] = %+v, want unavailable with no counts", w2)
	}
	if w2.Unavailable != "not_found" {
		t.Errorf("Repos[2].Unavailable = %q, want not_found", w2.Unavailable)
	}
	// Aggregated budget is the tightest across the successful repos.
	if facts.RateLimit == nil {
		t.Fatal("RateLimit = nil, want the tightest budget")
	}
	if facts.RateLimit.Remaining != 100 || !facts.RateLimit.ResetAt.Equal(r2) {
		t.Errorf("RateLimit = %+v, want {Remaining:100, ResetAt:%v}", facts.RateLimit, r2)
	}
}

// TestAuthoredActivityBatchThrottleAggregatesBudget pins the throttle-aware
// aggregation: one repo throttled, two others. The throttled repo degrades to a
// per-repo marker (the batch does NOT error), and the batch budget reports
// {Remaining:0, ResetAt:the throttle reset} so a caller is never told it has budget
// mid-throttle — the throttle-wins rule, which holds whether or not the other repos
// fetched. (That rule is also pinned deterministically at the unit level in
// internal/authored.) The other repos are asserted loosely on purpose: under
// backpressure a throttle stops new launches, so a not-yet-started repo may be
// skipped as not_attempted rather than carrying its counts — scheduler-dependent at
// concurrency > 1, and not what this test is about.
func TestAuthoredActivityBatchThrottleAggregatesBudget(t *testing.T) {
	reset := fixedClock.Add(15 * time.Minute)
	fetcher := authoredBatchFetcher(map[string]authoredCanned{
		"acme/a": {err: github.RateLimitedError{ResetAt: reset}},
		"acme/b": {result: withBudget(sixCounts(2, 0, 0, 0, 0, 0), 5000, fixedClock.Add(time.Hour))},
		"acme/c": {result: sixCounts(1, 0, 0, 0, 0, 0)}, // no budget
	})
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	res := callAuthoredActivityBatch(t, srv, map[string]any{
		"repos":  []any{"acme/a", "acme/b", "acme/c"},
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	})
	if res.IsError {
		t.Fatalf("IsError = true, want false (a throttled repo degrades, not the batch): %s", contentText(res))
	}
	facts := decodeBatchFacts(t, res)

	throttled := facts.Repos[0]
	if throttled.Available || throttled.Unavailable != "rate_limited" {
		t.Errorf("Repos[0] = %+v, want unavailable rate_limited", throttled)
	}
	if throttled.ResetAt == nil || !throttled.ResetAt.Equal(reset) {
		t.Errorf("Repos[0].ResetAt = %v, want %v", throttled.ResetAt, reset)
	}
	// The non-throttled repos either carried their counts or were skipped by
	// backpressure (not_attempted) — never a hard failure.
	for _, i := range []int{1, 2} {
		if r := facts.Repos[i]; !r.Available && r.Unavailable != authored.UnavailableNotAttempted {
			t.Errorf("Repos[%d] = %+v, want available with counts or not_attempted", i, r)
		}
	}
	if facts.RateLimit == nil {
		t.Fatal("RateLimit = nil, want the throttle recovery signal")
	}
	if facts.RateLimit.Remaining != 0 || !facts.RateLimit.ResetAt.Equal(reset) {
		t.Errorf("RateLimit = %+v, want {Remaining:0, ResetAt:%v} (throttle overrides a healthy budget)", facts.RateLimit, reset)
	}
}

// TestAuthoredActivityBatchDefaultsUntilToNow pins that an omitted until defaults
// to the bound clock's now, mirroring the single-repo tool.
func TestAuthoredActivityBatchDefaultsUntilToNow(t *testing.T) {
	fetcher := authoredBatchFetcher(map[string]authoredCanned{"acme/widgets": {result: sixCounts(1, 0, 0, 0, 0, 0)}})
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	facts := decodeBatchFacts(t, callAuthoredActivityBatch(t, srv, map[string]any{
		"repos":  []any{"acme/widgets"},
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	}))
	if !facts.Until.Equal(fixedClock) {
		t.Errorf("Until = %v, want the clock's now %v", facts.Until, fixedClock)
	}
}

// TestAuthoredActivityBatchUnresolvableAuthor pins the global-author contract: an
// unresolvable login fails every repo identically, so it surfaces as one
// whole-batch error naming the login — not N per-repo markers.
func TestAuthoredActivityBatchUnresolvableAuthor(t *testing.T) {
	fetcher := authoredBatchFetcher(map[string]authoredCanned{
		"acme/a": {err: github.ErrAuthorNotFound},
		"acme/b": {err: github.ErrAuthorNotFound},
	})
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	res := callAuthoredActivityBatch(t, srv, map[string]any{
		"repos":  []any{"acme/a", "acme/b"},
		"author": "nope",
		"since":  "2026-05-01T00:00:00Z",
	})
	if !res.IsError {
		t.Fatalf("IsError = false, want true for an unresolvable author")
	}
	if msg := contentText(res); !strings.Contains(msg, "nope") {
		t.Errorf("error %q does not name the unresolvable login", msg)
	}
}

// TestAuthoredActivityBatchValidatesInput pins pre-fetch validation: an empty or
// oversized repos list, a malformed or duplicate slug, a missing author, and a
// malformed or inverted window are each rejected before any fetch — surfacing as
// a transport-level schema rejection or a handler IsError.
func TestAuthoredActivityBatchValidatesInput(t *testing.T) {
	fetcher := authoredBatchFetcher(map[string]authoredCanned{"acme/widgets": {result: sixCounts(1, 0, 0, 0, 0, 0)}})

	oversized := make([]any, 51)
	for i := range oversized {
		oversized[i] = "acme/r" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"empty repos", map[string]any{"repos": []any{}, "author": "alice", "since": "2026-05-01T00:00:00Z"}},
		{"oversized repos", map[string]any{"repos": oversized, "author": "alice", "since": "2026-05-01T00:00:00Z"}},
		{"slug missing slash", map[string]any{"repos": []any{"acme"}, "author": "alice", "since": "2026-05-01T00:00:00Z"}},
		{"slug extra slash", map[string]any{"repos": []any{"a/b/c"}, "author": "alice", "since": "2026-05-01T00:00:00Z"}},
		{"slug blank half", map[string]any{"repos": []any{" / "}, "author": "alice", "since": "2026-05-01T00:00:00Z"}},
		{"duplicate slug case-insensitive", map[string]any{"repos": []any{"acme/widgets", "Acme/Widgets"}, "author": "alice", "since": "2026-05-01T00:00:00Z"}},
		{"missing author", map[string]any{"repos": []any{"acme/widgets"}, "since": "2026-05-01T00:00:00Z"}},
		{"unparseable since", map[string]any{"repos": []any{"acme/widgets"}, "author": "alice", "since": "last tuesday"}},
		{"until before since", map[string]any{"repos": []any{"acme/widgets"}, "author": "alice", "since": "2026-06-01T00:00:00Z", "until": "2026-05-01T00:00:00Z"}},
		{"empty window", map[string]any{"repos": []any{"acme/widgets"}, "author": "alice", "since": "2026-05-01T00:00:00Z", "until": "2026-05-01T00:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))
			res, err := rawCallAuthoredActivityBatch(t, srv, tc.args)
			if err == nil && (res == nil || !res.IsError) {
				t.Errorf("call succeeded, want a validation failure (transport error or IsError)")
			}
		})
	}
}

// TestAuthoredActivityBatchCancelledContextErrors pins that a cancelled request
// surfaces as a tool error, not a fabricated success built from the placeholder
// markers the fan-out stamps for not-yet-started repos. It drives the handler
// directly because cancellation can't be injected deterministically through the
// in-memory session.
func TestAuthoredActivityBatchCancelledContextErrors(t *testing.T) {
	fetcher := authoredBatchFetcher(map[string]authoredCanned{"acme/widgets": {result: sixCounts(1, 0, 0, 0, 0, 0)}})
	handler := authoredActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, authoredBatchConcurrency, authoredBatchPerRepoTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := handler(ctx, nil, authoredActivityBatchInput{
		Repos:  []string{"acme/widgets"},
		Author: "alice",
		Since:  "2026-05-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("err = nil, want a cancellation error (not a fabricated success)")
	}
}

// TestAuthoredActivityBatchCanonicalizesSlugWhitespace pins the intended forgiving
// behavior: a slug with whitespace around the slash is trimmed to its canonical
// owner/repo (GitHub slugs carry no spaces), so it matches the fetched repo and
// the entry echoes the canonical form.
func TestAuthoredActivityBatchCanonicalizesSlugWhitespace(t *testing.T) {
	fetcher := authoredBatchFetcher(map[string]authoredCanned{"acme/widgets": {result: sixCounts(7, 0, 0, 0, 0, 0)}})
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	facts := decodeBatchFacts(t, callAuthoredActivityBatch(t, srv, map[string]any{
		"repos":  []any{"  acme / widgets  "},
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	}))
	if len(facts.Repos) != 1 {
		t.Fatalf("len(Repos) = %d, want 1", len(facts.Repos))
	}
	if facts.Repos[0].Repo != "acme/widgets" || !facts.Repos[0].Available {
		t.Errorf("Repos[0] = %+v, want canonicalized acme/widgets available", facts.Repos[0])
	}
}

// TestAuthoredActivityBatchBackpressureStopsLaunchAfterThrottle pins the throttle
// backpressure contract: once any repo's fetch is rate-limited the batch stops
// launching new fetches rather than amplifying the throttle. Every repo is canned to
// throttle and concurrency is 1, so the first acquirer throttles and sets the stop
// before releasing its slot — exactly one fetch runs (asserted via the call counter,
// since which repo wins the slot is scheduler-chosen), the rest degrade to
// not_attempted, and the batch is not an error. The aggregated budget reports the
// throttle's recovery instant.
func TestAuthoredActivityBatchBackpressureStopsLaunchAfterThrottle(t *testing.T) {
	reset := fixedClock.Add(15 * time.Minute)
	var calls atomic.Int64
	fetcher := fakeFetcher{
		authoredCalls: &calls,
		authoredByRepo: map[string]authoredCanned{
			"acme/a": {err: github.RateLimitedError{ResetAt: reset}},
			"acme/b": {err: github.RateLimitedError{ResetAt: reset}},
			"acme/c": {err: github.RateLimitedError{ResetAt: reset}},
			"acme/d": {err: github.RateLimitedError{ResetAt: reset}},
		},
	}
	handler := authoredActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, 1, authoredBatchPerRepoTimeout)

	_, facts, err := handler(context.Background(), nil, authoredActivityBatchInput{
		Repos:  []string{"acme/a", "acme/b", "acme/c", "acme/d"},
		Author: "alice",
		Since:  "2026-05-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("fetch count = %d, want 1 (a throttle stops new launches)", got)
	}
	rateLimited, notAttempted := 0, 0
	for _, r := range facts.Repos {
		switch r.Unavailable {
		case authored.UnavailableRateLimited:
			rateLimited++
		case authored.UnavailableNotAttempted:
			notAttempted++
		default:
			t.Errorf("repo %s: unexpected marker %q (available=%v)", r.Repo, r.Unavailable, r.Available)
		}
	}
	if rateLimited != 1 || notAttempted != 3 {
		t.Errorf("markers = %d rate_limited / %d not_attempted, want 1 / 3", rateLimited, notAttempted)
	}
	if facts.RateLimit == nil || facts.RateLimit.Remaining != 0 || !facts.RateLimit.ResetAt.Equal(reset) {
		t.Errorf("RateLimit = %+v, want {Remaining:0, ResetAt:%v}", facts.RateLimit, reset)
	}
}

// TestAuthoredActivityBatchPerRepoTimeoutDegradesSlowRepo pins the per-repo deadline:
// a repo whose fetch hangs is bounded by a deadline derived from the batch context,
// so it degrades to its own fetch_failed marker (not a misclassification, not a
// whole-batch error) while a healthy repo running alongside it still returns counts.
// The timeout is generous (50ms) so a healthy repo's instant synchronous return
// cannot trip it, even under the race detector.
func TestAuthoredActivityBatchPerRepoTimeoutDegradesSlowRepo(t *testing.T) {
	fetcher := fakeFetcher{authoredByRepo: map[string]authoredCanned{
		"acme/slow": {block: true},
		"acme/fast": {result: sixCounts(3, 0, 0, 0, 0, 0)},
	}}
	handler := authoredActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, 2, 50*time.Millisecond)

	_, facts, err := handler(context.Background(), nil, authoredActivityBatchInput{
		Repos:  []string{"acme/slow", "acme/fast"},
		Author: "alice",
		Since:  "2026-05-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v (a per-repo timeout must not fail the batch)", err)
	}
	byRepo := make(map[string]authored.RepoActivity, len(facts.Repos))
	for _, r := range facts.Repos {
		byRepo[r.Repo] = r
	}
	if slow := byRepo["acme/slow"]; slow.Available || slow.Unavailable != authored.UnavailableFetchFailed {
		t.Errorf("slow repo = %+v, want unavailable fetch_failed", slow)
	}
	if fast := byRepo["acme/fast"]; !fast.Available || fast.Counts == nil || fast.Counts.CommitsAuthored.Count != 3 {
		t.Errorf("fast repo = %+v, want available with counts", fast)
	}
}

// TestAuthoredActivityBatchThrottlePreemptsAuthorEscalation pins the accepted
// interaction between backpressure and the whole-batch author contract: an
// unresolvable author normally escalates to one named error, but when a throttle
// stops the launch first, the author-not-found fetch never runs, so the batch returns
// a degraded partial (a throttle + not_attempted markers) rather than the author
// error. Keyed on call ordinal, not repo, so it does not depend on which repo wins
// the slot: the first fetch throttles; a later fetch would author-fail but is
// pre-empted.
func TestAuthoredActivityBatchThrottlePreemptsAuthorEscalation(t *testing.T) {
	reset := fixedClock.Add(10 * time.Minute)
	var calls atomic.Int64
	fetcher := fakeFetcher{
		authoredCalls: &calls,
		authoredSeq: func(call int64) authoredCanned {
			if call == 1 {
				return authoredCanned{err: github.RateLimitedError{ResetAt: reset}}
			}
			return authoredCanned{err: github.ErrAuthorNotFound}
		},
	}
	handler := authoredActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, 1, authoredBatchPerRepoTimeout)

	res, facts, err := handler(context.Background(), nil, authoredActivityBatchInput{
		Repos:  []string{"acme/a", "acme/b", "acme/c"},
		Author: "ghost",
		Since:  "2026-05-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("got whole-batch error %v, want a degraded partial (a throttle pre-empts author escalation)", err)
	}
	if res != nil && res.IsError {
		t.Fatal("IsError = true, want false")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("fetch count = %d, want 1 (the lurking author_not_found never ran)", got)
	}
	rateLimited, notAttempted := 0, 0
	for _, r := range facts.Repos {
		switch r.Unavailable {
		case authored.UnavailableRateLimited:
			rateLimited++
		case authored.UnavailableNotAttempted:
			notAttempted++
		default:
			t.Errorf("repo %s: unexpected marker %q (available=%v)", r.Repo, r.Unavailable, r.Available)
		}
	}
	if rateLimited != 1 || notAttempted != 2 {
		t.Errorf("markers = %d rate_limited / %d not_attempted, want 1 / 2", rateLimited, notAttempted)
	}
}

// TestAuthoredActivityBatchReposSerializeAsArray pins the non-null convention
// through the JSON round-trip: the per-repo entry list is always an array, never
// null, so a client never special-cases a null.
func TestAuthoredActivityBatchReposSerializeAsArray(t *testing.T) {
	fetcher := authoredBatchFetcher(map[string]authoredCanned{"acme/widgets": {result: sixCounts(1, 0, 0, 0, 0, 0)}})
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	res := callAuthoredActivityBatch(t, srv, map[string]any{
		"repos":  []any{"acme/widgets"},
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	})
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if !strings.Contains(string(raw), `"repos":[`) {
		t.Errorf("repos did not serialize as an array: %s", raw)
	}
}

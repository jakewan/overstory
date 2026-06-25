package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/maintenance"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func rawCallMaintenanceActivityBatch(t *testing.T, srv *mcp.Server, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	cs := connect(t, srv)
	return cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "maintenance_activity_batch", Arguments: args})
}

func callMaintenanceActivityBatch(t *testing.T, srv *mcp.Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := rawCallMaintenanceActivityBatch(t, srv, args)
	if err != nil {
		t.Fatalf("call maintenance_activity_batch: %v", err)
	}
	return res
}

func decodeMaintenanceBatchFacts(t *testing.T, res *mcp.CallToolResult) maintenance.BatchFacts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts maintenance.BatchFacts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("unmarshal maintenance batch facts: %v", err)
	}
	return facts
}

func eventsWithBudget(events []github.IssueEvent, remaining int, reset time.Time) github.IssueEventsResult {
	return github.IssueEventsResult{Events: events, RateLimit: &github.RateLimit{Remaining: remaining, ResetAt: reset}}
}

// TestMaintenanceActivityBatchSurfacesPerRepoResults pins the core batch contract:
// repos fan out to per-repo entries in request order, a successful repo carries its
// grouped items (filtered to the actor), a failing repo degrades to a marker, and
// the actor/window/identity are echoed once.
func TestMaintenanceActivityBatchSurfacesPerRepoResults(t *testing.T) {
	at := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	r := fixedClock.Add(30 * time.Minute)
	fetcher := fakeFetcher{eventsByRepo: map[string]eventsCanned{
		"acme/widgets":  {result: eventsWithBudget([]github.IssueEvent{mEvent(1, "labeled", "jakewan", at, 100, false)}, 4900, r)},
		"ghost/missing": {err: github.ErrRepoNotFound},
	}}
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMaintenanceBatchFacts(t, callMaintenanceActivityBatch(t, srv, map[string]any{
		"repos":  []any{"acme/widgets", "ghost/missing"},
		"author": "jakewan",
		"since":  "2026-05-01T00:00:00Z",
	}))

	if facts.Author != "jakewan" || !facts.GeneratedAt.Equal(fixedClock) {
		t.Errorf("identity = {%q, %v}, want {jakewan, %v}", facts.Author, facts.GeneratedAt, fixedClock)
	}
	if len(facts.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(facts.Repos))
	}
	if facts.Repos[0].Repo != "acme/widgets" || facts.Repos[1].Repo != "ghost/missing" {
		t.Errorf("order = [%q,%q], want [acme/widgets, ghost/missing]", facts.Repos[0].Repo, facts.Repos[1].Repo)
	}
	w := facts.Repos[0]
	if !w.Available || len(w.Items) != 1 || w.Items[0].Number != 100 {
		t.Errorf("Repos[0] = %+v, want available with item 100", w)
	}
	m := facts.Repos[1]
	if m.Available || m.Unavailable != "not_found" {
		t.Errorf("Repos[1] = %+v, want unavailable not_found", m)
	}
	if facts.RateLimit == nil || facts.RateLimit.Remaining != 4900 {
		t.Errorf("RateLimit = %+v, want Remaining 4900", facts.RateLimit)
	}
}

// TestMaintenanceActivityBatchUnknownAuthorYieldsZeroItems pins the absence of the
// authored batch's whole-batch author error: an actor unknown to every repo yields
// zero items per repo (each repo still available), never a batch error.
func TestMaintenanceActivityBatchUnknownAuthorYieldsZeroItems(t *testing.T) {
	at := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{eventsByRepo: map[string]eventsCanned{
		"acme/a": {result: github.IssueEventsResult{Events: []github.IssueEvent{mEvent(1, "labeled", "someoneelse", at, 1, false)}}},
		"acme/b": {result: github.IssueEventsResult{Events: []github.IssueEvent{mEvent(2, "closed", "anotherperson", at, 2, false)}}},
	}}
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	res := callMaintenanceActivityBatch(t, srv, map[string]any{
		"repos": []any{"acme/a", "acme/b"}, "author": "ghost", "since": "2026-05-01T00:00:00Z",
	})
	if res.IsError {
		t.Fatalf("IsError = true, want false (an unknown actor is not a batch error): %s", contentText(res))
	}
	facts := decodeMaintenanceBatchFacts(t, res)
	for i, r := range facts.Repos {
		if !r.Available {
			t.Errorf("Repos[%d] = %+v, want available", i, r)
		}
		if len(r.Items) != 0 {
			t.Errorf("Repos[%d] has %d items, want 0 (actor matched nothing)", i, len(r.Items))
		}
	}
}

// TestMaintenanceActivityBatchBackpressureStopsLaunchAfterThrottle pins the
// throttle backpressure: every repo is canned to throttle and concurrency is 1, so
// the first acquirer throttles and sets the stop — exactly one fetch runs (asserted
// via the call counter), the rest degrade to not_attempted, and the batch is not an
// error.
func TestMaintenanceActivityBatchBackpressureStopsLaunchAfterThrottle(t *testing.T) {
	reset := fixedClock.Add(15 * time.Minute)
	var calls atomic.Int64
	fetcher := fakeFetcher{
		eventsCalls: &calls,
		eventsByRepo: map[string]eventsCanned{
			"acme/a": {err: github.RateLimitedError{ResetAt: reset}},
			"acme/b": {err: github.RateLimitedError{ResetAt: reset}},
			"acme/c": {err: github.RateLimitedError{ResetAt: reset}},
			"acme/d": {err: github.RateLimitedError{ResetAt: reset}},
		},
	}
	handler := maintenanceActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, 1, maintenanceBatchPerRepoTimeout)

	_, facts, err := handler(context.Background(), nil, maintenanceActivityBatchInput{
		Repos: []string{"acme/a", "acme/b", "acme/c", "acme/d"}, Author: "jakewan", Since: "2026-05-01T00:00:00Z",
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
		case maintenance.UnavailableRateLimited:
			rateLimited++
		case maintenance.UnavailableNotAttempted:
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

// TestMaintenanceActivityBatchPerRepoTimeoutDegradesSlowRepo pins the per-repo
// deadline: a repo whose fetch hangs degrades to its own fetch_failed while a
// healthy repo alongside it still returns its items.
func TestMaintenanceActivityBatchPerRepoTimeoutDegradesSlowRepo(t *testing.T) {
	at := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{eventsByRepo: map[string]eventsCanned{
		"acme/slow": {block: true},
		"acme/fast": {result: github.IssueEventsResult{Events: []github.IssueEvent{mEvent(1, "labeled", "jakewan", at, 7, false)}}},
	}}
	handler := maintenanceActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, 2, 50*time.Millisecond)

	_, facts, err := handler(context.Background(), nil, maintenanceActivityBatchInput{
		Repos: []string{"acme/slow", "acme/fast"}, Author: "jakewan", Since: "2026-05-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v (a per-repo timeout must not fail the batch)", err)
	}
	byRepo := make(map[string]maintenance.RepoActivity, len(facts.Repos))
	for _, r := range facts.Repos {
		byRepo[r.Repo] = r
	}
	if slow := byRepo["acme/slow"]; slow.Available || slow.Unavailable != maintenance.UnavailableFetchFailed {
		t.Errorf("slow repo = %+v, want unavailable fetch_failed", slow)
	}
	if fast := byRepo["acme/fast"]; !fast.Available || len(fast.Items) != 1 || fast.Items[0].Number != 7 {
		t.Errorf("fast repo = %+v, want available with item 7", fast)
	}
}

// TestMaintenanceActivityBatchCancelledContextErrors pins that a cancelled request
// surfaces as a tool error, not a fabricated success built from placeholder markers.
func TestMaintenanceActivityBatchCancelledContextErrors(t *testing.T) {
	fetcher := fakeFetcher{eventsByRepo: map[string]eventsCanned{"acme/widgets": {}}}
	handler := maintenanceActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, maintenanceBatchConcurrency, maintenanceBatchPerRepoTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := handler(ctx, nil, maintenanceActivityBatchInput{
		Repos: []string{"acme/widgets"}, Author: "jakewan", Since: "2026-05-01T00:00:00Z",
	})
	if err == nil {
		t.Fatal("err = nil, want a cancellation error (not a fabricated success)")
	}
}

// TestMaintenanceActivityBatchValidatesInput pins pre-fetch validation, mirroring
// the authored batch: an empty or oversized list, a malformed or duplicate slug, a
// missing actor, and a malformed or inverted window are each rejected before any
// fetch.
func TestMaintenanceActivityBatchValidatesInput(t *testing.T) {
	fetcher := fakeFetcher{eventsByRepo: map[string]eventsCanned{"acme/widgets": {}}}
	oversized := make([]any, 51)
	for i := range oversized {
		oversized[i] = "acme/r" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"empty repos", map[string]any{"repos": []any{}, "author": "a", "since": "2026-05-01T00:00:00Z"}},
		{"oversized repos", map[string]any{"repos": oversized, "author": "a", "since": "2026-05-01T00:00:00Z"}},
		{"slug missing slash", map[string]any{"repos": []any{"acme"}, "author": "a", "since": "2026-05-01T00:00:00Z"}},
		{"duplicate slug", map[string]any{"repos": []any{"acme/widgets", "Acme/Widgets"}, "author": "a", "since": "2026-05-01T00:00:00Z"}},
		{"missing author", map[string]any{"repos": []any{"acme/widgets"}, "since": "2026-05-01T00:00:00Z"}},
		{"unparseable since", map[string]any{"repos": []any{"acme/widgets"}, "author": "a", "since": "nope"}},
		{"until before since", map[string]any{"repos": []any{"acme/widgets"}, "author": "a", "since": "2026-06-01T00:00:00Z", "until": "2026-05-01T00:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))
			res, err := rawCallMaintenanceActivityBatch(t, srv, tc.args)
			if err == nil && (res == nil || !res.IsError) {
				t.Errorf("call succeeded, want a validation failure (transport error or IsError)")
			}
		})
	}
}

// TestMaintenanceActivityBatchReposSerializeAsArray pins the non-null convention
// through the JSON round-trip: the per-repo entry list is always an array.
func TestMaintenanceActivityBatchReposSerializeAsArray(t *testing.T) {
	fetcher := fakeFetcher{eventsByRepo: map[string]eventsCanned{"acme/widgets": {}}}
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	res := callMaintenanceActivityBatch(t, srv, map[string]any{
		"repos": []any{"acme/widgets"}, "author": "jakewan", "since": "2026-05-01T00:00:00Z",
	})
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if !strings.Contains(string(raw), `"repos":[`) {
		t.Errorf("repos did not serialize as an array: %s", raw)
	}
}

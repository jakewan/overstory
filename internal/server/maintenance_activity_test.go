package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/maintenance"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mEvent builds an issue event for the maintenance fakes: a monotonic id, an
// actor, an instant, and the item it touched; payload fields are set by the caller.
func mEvent(id int64, typ, actor string, at time.Time, num int, isPR bool) github.IssueEvent {
	return github.IssueEvent{
		EventID:     id,
		Type:        typ,
		Actor:       actor,
		CreatedAt:   at,
		IssueNumber: num,
		IssueTitle:  "item",
		IssueIsPR:   isPR,
	}
}

func rawCallMaintenanceActivity(t *testing.T, srv *mcp.Server, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	cs := connect(t, srv)
	return cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "maintenance_activity", Arguments: args})
}

func callMaintenanceActivity(t *testing.T, srv *mcp.Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := rawCallMaintenanceActivity(t, srv, args)
	if err != nil {
		t.Fatalf("call maintenance_activity: %v", err)
	}
	return res
}

func decodeMaintenanceFacts(t *testing.T, res *mcp.CallToolResult) maintenance.Facts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts maintenance.Facts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("unmarshal maintenance facts: %v", err)
	}
	return facts
}

// TestMaintenanceActivityGroupsAndStampsFacts pins the core contract through the
// in-memory session: the actor's in-window mutations group by item, the
// review-level identity (repo, actor, window, generatedAt) is stamped, and the
// REST budget surfaces on rateLimit.
func TestMaintenanceActivityGroupsAndStampsFacts(t *testing.T) {
	at := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	reset := fixedClock.Add(time.Hour)
	labeled := mEvent(2, "labeled", "alice", at, 100, false)
	labeled.Label = "reductions"
	closed := mEvent(3, "closed", "alice", at.Add(time.Hour), 100, false)
	fetcher := fakeFetcher{eventsResult: github.IssueEventsResult{
		Events:    []github.IssueEvent{labeled, closed},
		RateLimit: &github.RateLimit{Remaining: 4900, ResetAt: reset},
	}}
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMaintenanceFacts(t, callMaintenanceActivity(t, srv, map[string]any{
		"owner":  "acme",
		"repo":   "widgets",
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	}))

	if facts.Repo != "acme/widgets" {
		t.Errorf("Repo = %q, want acme/widgets", facts.Repo)
	}
	if facts.Author != "alice" {
		t.Errorf("Author = %q, want alice", facts.Author)
	}
	if !facts.GeneratedAt.Equal(fixedClock) {
		t.Errorf("GeneratedAt = %v, want %v", facts.GeneratedAt, fixedClock)
	}
	if !facts.Until.Equal(fixedClock) {
		t.Errorf("Until = %v, want the clock's now %v (omitted until defaults to now)", facts.Until, fixedClock)
	}
	if len(facts.Items) != 1 || facts.Items[0].Number != 100 {
		t.Fatalf("Items = %+v, want one item for issue 100", facts.Items)
	}
	if got := len(facts.Items[0].Events); got != 2 {
		t.Fatalf("issue 100 has %d events, want 2", got)
	}
	if facts.Items[0].Events[0].Type != "labeled" || facts.Items[0].Events[0].Label != "reductions" {
		t.Errorf("first event = %+v, want labeled/reductions", facts.Items[0].Events[0])
	}
	if facts.RateLimit == nil || facts.RateLimit.Remaining != 4900 {
		t.Errorf("RateLimit = %+v, want Remaining 4900 (REST core pool)", facts.RateLimit)
	}
}

// TestMaintenanceActivityUnknownAuthorYieldsZeroItems pins the no-resolution
// contract: an actor that performed no mutation yields an empty (non-nil) item
// list, never an error — distinct from authored_activity's author-not-found.
func TestMaintenanceActivityUnknownAuthorYieldsZeroItems(t *testing.T) {
	at := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{eventsResult: github.IssueEventsResult{
		Events:    []github.IssueEvent{mEvent(1, "labeled", "someoneelse", at, 1, false)},
		RateLimit: &github.RateLimit{Remaining: 5000, ResetAt: fixedClock.Add(time.Hour)},
	}}
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	res := callMaintenanceActivity(t, srv, map[string]any{
		"owner": "acme", "repo": "widgets", "author": "ghost", "since": "2026-05-01T00:00:00Z",
	})
	if res.IsError {
		t.Fatalf("IsError = true, want false (an unknown actor is not an error): %s", contentText(res))
	}
	facts := decodeMaintenanceFacts(t, res)
	if facts.Items == nil {
		t.Error("Items = nil, want a non-nil empty slice")
	}
	if len(facts.Items) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(facts.Items))
	}
}

// TestMaintenanceActivityThrottleSurfacesRetry pins that a throttle on the fetch
// surfaces as a tool error naming the retry instant, mirroring the other tools.
func TestMaintenanceActivityThrottleSurfacesRetry(t *testing.T) {
	reset := fixedClock.Add(20 * time.Minute)
	fetcher := fakeFetcher{eventsErr: github.RateLimitedError{ResetAt: reset}}
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))

	res := callMaintenanceActivity(t, srv, map[string]any{
		"owner": "acme", "repo": "widgets", "author": "alice", "since": "2026-05-01T00:00:00Z",
	})
	if !res.IsError {
		t.Fatal("IsError = false, want true for a throttled fetch")
	}
	if msg := contentText(res); !strings.Contains(msg, reset.UTC().Format(time.RFC3339)) {
		t.Errorf("error %q does not name the retry instant", msg)
	}
}

// TestMaintenanceActivityValidatesInput pins pre-fetch validation: a missing
// owner/repo/author and a malformed or inverted window are each rejected before any
// fetch — surfacing as a transport-level schema rejection or a handler IsError.
func TestMaintenanceActivityValidatesInput(t *testing.T) {
	fetcher := fakeFetcher{}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"missing owner", map[string]any{"repo": "widgets", "author": "a", "since": "2026-05-01T00:00:00Z"}},
		{"missing repo", map[string]any{"owner": "acme", "author": "a", "since": "2026-05-01T00:00:00Z"}},
		{"missing author", map[string]any{"owner": "acme", "repo": "widgets", "since": "2026-05-01T00:00:00Z"}},
		{"unparseable since", map[string]any{"owner": "acme", "repo": "widgets", "author": "a", "since": "last week"}},
		{"until before since", map[string]any{"owner": "acme", "repo": "widgets", "author": "a", "since": "2026-06-01T00:00:00Z", "until": "2026-05-01T00:00:00Z"}},
		{"empty window", map[string]any{"owner": "acme", "repo": "widgets", "author": "a", "since": "2026-05-01T00:00:00Z", "until": "2026-05-01T00:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }))
			res, err := rawCallMaintenanceActivity(t, srv, tc.args)
			if err == nil && (res == nil || !res.IsError) {
				t.Errorf("call succeeded, want a validation failure (transport error or IsError)")
			}
		})
	}
}

// TestMaintenanceActivityReadsNoManifest pins the manifest-blind contract: the
// tool returns its facts with no manifest configured at all, so it never fails on
// a manifest-resolution path the way a convention-driven tool would.
func TestMaintenanceActivityReadsNoManifest(t *testing.T) {
	at := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{eventsResult: github.IssueEventsResult{
		Events: []github.IssueEvent{mEvent(1, "labeled", "alice", at, 1, false)},
	}}
	// No WithManifestRoot/WithManifestFiles: discovery would fall through to defaults,
	// and the tool must not depend on any manifest match.
	srv := New(WithFetcher(fetcher), WithClock(func() time.Time { return fixedClock }), WithManifestFiles([]string{}))

	facts := decodeMaintenanceFacts(t, callMaintenanceActivity(t, srv, map[string]any{
		"owner": "unknown", "repo": "unconfigured", "author": "alice", "since": "2026-05-01T00:00:00Z",
	}))
	if len(facts.Items) != 1 {
		t.Errorf("len(Items) = %d, want 1 (manifest-blind tool still reduces)", len(facts.Items))
	}
}

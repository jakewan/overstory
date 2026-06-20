package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/authored"
	"github.com/jakewan/overstory/internal/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callAuthoredActivity drives the tool through the in-memory MCP session and
// returns the raw result so error-path cases can assert on IsError.
func callAuthoredActivity(t *testing.T, srv *mcp.Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "authored_activity",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call authored_activity: %v", err)
	}
	return res
}

func decodeAuthoredFacts(t *testing.T, res *mcp.CallToolResult) authored.Facts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts authored.Facts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("unmarshal facts: %v", err)
	}
	return facts
}

// authoredFetcher is a fakeFetcher carrying canned authored-activity results; the
// authored_activity tool reads no manifest, so these tests need no manifest dir.
func authoredFetcher(result github.AuthoredActivityResult, err error) fakeFetcher {
	return fakeFetcher{authoredResult: result, authoredErr: err}
}

// TestAuthoredActivitySurfacesSixDecomposedCounts pins the happy path: each of
// the six author/engagement counts surfaces with its own per-category fidelity
// label, and the window/author/identity are echoed for the consumer.
func TestAuthoredActivitySurfacesSixDecomposedCounts(t *testing.T) {
	result := github.AuthoredActivityResult{
		CommitsAuthored:     12,
		IssuesOpened:        3,
		PullRequestsOpened:  5,
		ReviewsSubmitted:    7,
		PullRequestsEngaged: 9,
		IssuesEngaged:       4,
	}
	srv := New(WithFetcher(authoredFetcher(result, nil)), WithClock(func() time.Time { return fixedClock }))

	facts := decodeAuthoredFacts(t, callAuthoredActivity(t, srv, map[string]any{
		"owner":  "acme",
		"repo":   "widgets",
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
		"until":  "2026-06-01T00:00:00Z",
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
	for _, tc := range []struct {
		name string
		got  authored.Count
		want int
	}{
		{"commitsAuthored", facts.Counts.CommitsAuthored, 12},
		{"issuesOpened", facts.Counts.IssuesOpened, 3},
		{"pullRequestsOpened", facts.Counts.PullRequestsOpened, 5},
		{"reviewsSubmitted", facts.Counts.ReviewsSubmitted, 7},
		{"pullRequestsEngaged", facts.Counts.PullRequestsEngaged, 9},
		{"issuesEngaged", facts.Counts.IssuesEngaged, 4},
	} {
		if tc.got.Count != tc.want {
			t.Errorf("%s count = %d, want %d", tc.name, tc.got.Count, tc.want)
		}
		if strings.TrimSpace(tc.got.Fidelity) == "" {
			t.Errorf("%s carries no fidelity label", tc.name)
		}
	}
}

// TestAuthoredActivityDefaultsUntilToNow pins that an omitted `until` defaults to
// the bound clock's now, so a caller can pass only a window start.
func TestAuthoredActivityDefaultsUntilToNow(t *testing.T) {
	srv := New(WithFetcher(authoredFetcher(github.AuthoredActivityResult{}, nil)), WithClock(func() time.Time { return fixedClock }))

	facts := decodeAuthoredFacts(t, callAuthoredActivity(t, srv, map[string]any{
		"owner":  "acme",
		"repo":   "widgets",
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	}))

	if !facts.Until.Equal(fixedClock) {
		t.Errorf("Until = %v, want the clock's now %v", facts.Until, fixedClock)
	}
}

// TestAuthoredActivityRepoNotFound pins that a fetch failure surfaces as a tool
// error naming the repo, not a silent zero-count result.
func TestAuthoredActivityRepoNotFound(t *testing.T) {
	srv := New(WithFetcher(authoredFetcher(github.AuthoredActivityResult{}, github.ErrRepoNotFound)), WithClock(func() time.Time { return fixedClock }))

	res := callAuthoredActivity(t, srv, map[string]any{
		"owner":  "ghost",
		"repo":   "missing",
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	})
	if !res.IsError {
		t.Fatalf("IsError = false, want true for repo-not-found")
	}
	if msg := contentText(res); !strings.Contains(msg, "ghost/missing") {
		t.Errorf("error %q does not name the repo ghost/missing", msg)
	}
}

// TestAuthoredActivityRateLimitedNamesResetInstant pins the all-or-nothing
// degradation contract's recovery signal: a throttle surfaces as a tool error
// naming the absolute retry instant, not a partial count.
func TestAuthoredActivityRateLimitedNamesResetInstant(t *testing.T) {
	reset := fixedClock.Add(15 * time.Minute)
	srv := New(WithFetcher(authoredFetcher(github.AuthoredActivityResult{}, github.RateLimitedError{ResetAt: reset})), WithClock(func() time.Time { return fixedClock }))

	res := callAuthoredActivity(t, srv, map[string]any{
		"owner":  "acme",
		"repo":   "widgets",
		"author": "alice",
		"since":  "2026-05-01T00:00:00Z",
	})
	if !res.IsError {
		t.Fatalf("IsError = false, want true for a throttled fetch")
	}
	if msg := contentText(res); !strings.Contains(msg, reset.UTC().Format(time.RFC3339)) {
		t.Errorf("error %q does not name the reset instant %s", msg, reset.UTC().Format(time.RFC3339))
	}
}

// TestAuthoredActivityUnknownAuthor pins that an unresolved login surfaces as a
// named error (not six silent zeros indistinguishable from an inactive user).
func TestAuthoredActivityUnknownAuthor(t *testing.T) {
	srv := New(WithFetcher(authoredFetcher(github.AuthoredActivityResult{}, github.ErrAuthorNotFound)), WithClock(func() time.Time { return fixedClock }))

	res := callAuthoredActivity(t, srv, map[string]any{
		"owner":  "acme",
		"repo":   "widgets",
		"author": "nope",
		"since":  "2026-05-01T00:00:00Z",
	})
	if !res.IsError {
		t.Fatalf("IsError = false, want true for unknown author")
	}
}

// TestAuthoredActivityValidatesInput pins the handler's pre-fetch validation:
// missing required fields and a malformed or inverted window are rejected with a
// tool error before any fetch runs.
func TestAuthoredActivityValidatesInput(t *testing.T) {
	srv := New(WithFetcher(authoredFetcher(github.AuthoredActivityResult{}, nil)), WithClock(func() time.Time { return fixedClock }))

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"missing author", map[string]any{"owner": "acme", "repo": "widgets", "since": "2026-05-01T00:00:00Z"}},
		{"unparseable since", map[string]any{"owner": "acme", "repo": "widgets", "author": "alice", "since": "last tuesday"}},
		{"until before since", map[string]any{"owner": "acme", "repo": "widgets", "author": "alice", "since": "2026-06-01T00:00:00Z", "until": "2026-05-01T00:00:00Z"}},
		{"empty window", map[string]any{"owner": "acme", "repo": "widgets", "author": "alice", "since": "2026-05-01T00:00:00Z", "until": "2026-05-01T00:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := callAuthoredActivity(t, srv, tc.args)
			if !res.IsError {
				t.Errorf("IsError = false, want true for %s", tc.name)
			}
		})
	}
}

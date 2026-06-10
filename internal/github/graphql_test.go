package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type staticToken struct {
	token string
	err   error
}

func (s staticToken) Token(_ context.Context) (string, error) { return s.token, s.err }

// fetcherTo builds a GraphQLFetcher pointed at a test server with a static
// token — no real gh, no real network.
func fetcherTo(url, token string) *GraphQLFetcher {
	return &GraphQLFetcher{endpoint: url, tokens: staticToken{token: token}, client: &http.Client{}}
}

func jsonServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListOpenIssuesDerivesLastHumanActivity(t *testing.T) {
	body := `{"data":{"repository":{"issues":{
		"totalCount":2,
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"number":1,"title":"a","url":"ua","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[
				{"createdAt":"2025-06-01T00:00:00Z","author":{"__typename":"User","login":"alice"}},
				{"createdAt":"2025-07-01T00:00:00Z","author":{"__typename":"Bot","login":"dependabot[bot]"}}
			]}},
			{"number":2,"title":"b","url":"ub","createdAt":"2025-03-01T00:00:00Z","comments":{"nodes":[]}}
		]
	}}}}`
	srv := jsonServer(t, http.StatusOK, body)
	res, err := fetcherTo(srv.URL, "tok").ListOpenIssues(context.Background(), "acme/widgets", 100)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if res.TotalOpen != 2 || len(res.Issues) != 2 {
		t.Fatalf("TotalOpen=%d len=%d, want 2/2", res.TotalOpen, len(res.Issues))
	}
	// Issue 1's newest comment is a bot; the last *human* comment is the signal.
	wantHuman := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	if !res.Issues[0].LastActivityAt.Equal(wantHuman) {
		t.Errorf("issue 1 LastActivityAt = %v, want %v (bot comment ignored)", res.Issues[0].LastActivityAt, wantHuman)
	}
	// Issue 2 has no comments; falls back to creation.
	wantCreate := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	if !res.Issues[1].LastActivityAt.Equal(wantCreate) {
		t.Errorf("issue 2 LastActivityAt = %v, want %v (creation fallback)", res.Issues[1].LastActivityAt, wantCreate)
	}
}

func TestListOpenIssuesParsesLabels(t *testing.T) {
	// Two assertions in one round-trip: (a) the query actually asks GitHub for
	// labels — the query string is otherwise untested, so a typo in the selection
	// would silently ship and the deferred reduction would see no labels; (b) the
	// returned label names decode onto Issue.Labels.
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		body := `{"data":{"repository":{"issues":{
			"totalCount":1,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"title":"a","url":"ua","createdAt":"2025-01-01T00:00:00Z",
				 "labels":{"nodes":[{"name":"deferred"},{"name":"bug"}]},
				 "comments":{"nodes":[]}}
			]
		}}}}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListOpenIssues(context.Background(), "acme/widgets", 100)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	// Match the selection token, not exact spacing, to avoid brittleness.
	if !strings.Contains(gotQuery, "labels(") {
		t.Errorf("query does not request labels; got:\n%s", gotQuery)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(res.Issues))
	}
	got := res.Issues[0].Labels
	if len(got) != 2 || got[0] != "deferred" || got[1] != "bug" {
		t.Errorf("Labels = %v, want [deferred bug]", got)
	}
}

func TestListOpenIssuesPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				After *string `json:"after"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		var body string
		if req.Variables.After == nil {
			body = `{"data":{"repository":{"issues":{"totalCount":3,"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
				{"number":1,"createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]}},
				{"number":2,"createdAt":"2025-01-02T00:00:00Z","comments":{"nodes":[]}}
			]}}}}`
		} else {
			body = `{"data":{"repository":{"issues":{"totalCount":3,"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"nodes":[
				{"number":3,"createdAt":"2025-01-03T00:00:00Z","comments":{"nodes":[]}}
			]}}}}`
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListOpenIssues(context.Background(), "acme/widgets", 250)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(res.Issues) != 3 {
		t.Errorf("collected %d issues across pages, want 3", len(res.Issues))
	}
}

func TestListOpenIssuesErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, ``, ErrGHNotAuthed},
		{"rate limited", http.StatusTooManyRequests, ``, ErrRateLimited},
		{"not found status", http.StatusNotFound, ``, ErrRepoNotFound},
		{"graphql not found", http.StatusOK, `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"x"}]}`, ErrRepoNotFound},
		{"null repository", http.StatusOK, `{"data":{"repository":null}}`, ErrRepoNotFound},
		{"graphql rate limited", http.StatusOK, `{"errors":[{"type":"RATE_LIMITED","message":"x"}]}`, ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := jsonServer(t, tc.status, tc.body)
			_, err := fetcherTo(srv.URL, "tok").ListOpenIssues(context.Background(), "acme/widgets", 100)
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestListOpenIssuesNeverLeaksToken(t *testing.T) {
	const secret = "super-secret-token"
	srv := jsonServer(t, http.StatusNotFound, ``)
	_, err := fetcherTo(srv.URL, secret).ListOpenIssues(context.Background(), "acme/widgets", 100)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error message leaks the token: %q", err)
	}
}

func TestListOpenIssuesPropagatesTokenError(t *testing.T) {
	f := &GraphQLFetcher{endpoint: "http://unused", tokens: staticToken{err: ErrGHNotAuthed}, client: &http.Client{}}
	_, err := f.ListOpenIssues(context.Background(), "acme/widgets", 100)
	if !errors.Is(err, ErrGHNotAuthed) {
		t.Errorf("error = %v, want ErrGHNotAuthed", err)
	}
}

func TestListOpenIssuesRejectsMalformedOwnerRepo(t *testing.T) {
	f := fetcherTo("http://unused", "tok")
	if _, err := f.ListOpenIssues(context.Background(), "justaname", 100); err == nil {
		t.Error("accepted malformed owner/repo, want error")
	}
}

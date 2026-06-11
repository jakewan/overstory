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

// jsonServerWithHeaders is jsonServer plus response headers, for the rate-limit
// recovery-signal tests. Every header is set before WriteHeader — net/http drops
// any header set after the status is written, which would silently false-green
// the populated cases on the no-signal assertions.
func jsonServerWithHeaders(t *testing.T, status int, headers map[string]string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
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

func TestListOpenIssuesParsesBodyText(t *testing.T) {
	// (a) the query actually asks GitHub for bodyText — a typo in the selection
	// would silently ship and the quality reduction would see every body as empty;
	// (b) the returned bodyText decodes onto Issue.BodyText.
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
				 "bodyText":"a real description",
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
	if !strings.Contains(gotQuery, "bodyText") {
		t.Errorf("query does not request bodyText; got:\n%s", gotQuery)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(res.Issues))
	}
	if res.Issues[0].BodyText != "a real description" {
		t.Errorf("BodyText = %q, want %q", res.Issues[0].BodyText, "a real description")
	}
}

func TestListOpenIssuesParsesCrossReferences(t *testing.T) {
	// (a) the query asks GitHub for the cross-reference timeline — a typo in the
	// selection would silently ship and the cross-reference reduction would see no
	// edges; (b) the events decode onto Issue.ReferencedBy with the issue-to-issue,
	// same-repo filtering applied. The first node's payload mirrors a real capture
	// of jakewan/overstory#1 (an Issue source #9 alongside PullRequest sources),
	// augmented with a cross-repository event and a duplicate to pin the filters.
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		// Issue 1: totalCount 5 but only 4 nodes returned → truncated. Kept source:
		// Issue #9. Dropped: PullRequest #2, cross-repo Issue #7, duplicate Issue #9.
		// Issue 2: timelineItems is null. Issue 3: the field is absent entirely.
		body := `{"data":{"repository":{"issues":{
			"totalCount":3,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"title":"a","url":"ua","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "timelineItems":{"totalCount":5,"nodes":[
					{"__typename":"CrossReferencedEvent","isCrossRepository":false,"source":{"__typename":"Issue","number":9}},
					{"__typename":"CrossReferencedEvent","isCrossRepository":false,"source":{"__typename":"PullRequest","number":2}},
					{"__typename":"CrossReferencedEvent","isCrossRepository":true,"source":{"__typename":"Issue","number":7}},
					{"__typename":"CrossReferencedEvent","isCrossRepository":false,"source":{"__typename":"Issue","number":9}}
				 ]}},
				{"number":2,"title":"b","url":"ub","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "timelineItems":null},
				{"number":3,"title":"c","url":"uc","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]}}
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
	if !strings.Contains(gotQuery, "timelineItems") || !strings.Contains(gotQuery, "CROSS_REFERENCED_EVENT") {
		t.Errorf("query does not request the cross-reference timeline; got:\n%s", gotQuery)
	}
	if len(res.Issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(res.Issues))
	}

	// Issue 1: PR source, cross-repo source, and duplicate all dropped; #9 kept.
	if got := res.Issues[0].ReferencedBy; len(got) != 1 || got[0] != 9 {
		t.Errorf("issue 1 ReferencedBy = %v, want [9]", got)
	}
	if !res.Issues[0].CrossRefsTruncated {
		t.Error("issue 1 CrossRefsTruncated = false, want true (totalCount 5 > 4 nodes)")
	}
	// Issues 2 (null) and 3 (absent): no references, not truncated, no panic.
	for _, i := range []int{1, 2} {
		if got := res.Issues[i].ReferencedBy; len(got) != 0 {
			t.Errorf("issue %d ReferencedBy = %v, want empty", res.Issues[i].Number, got)
		}
		if res.Issues[i].CrossRefsTruncated {
			t.Errorf("issue %d CrossRefsTruncated = true, want false", res.Issues[i].Number)
		}
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

// TestListOpenIssuesRateLimitCarriesResetSignal pins that a throttle returns the
// typed RateLimitedError with the reset signal parsed from GitHub's response —
// X-RateLimit-Reset (epoch) and Retry-After (seconds or HTTP-date) from headers,
// or the GraphQL body's resetAt as a fallback on the secondary-limit path where
// headers are often absent. With no signal at all it still classifies as a rate
// limit, just with a zero-value reset.
func TestListOpenIssuesRateLimitCarriesResetSignal(t *testing.T) {
	const epoch = 1780000000
	httpDate := time.Date(2026, 6, 10, 0, 15, 0, 0, time.UTC)
	for _, tc := range []struct {
		name           string
		status         int
		headers        map[string]string
		body           string
		wantResetAt    time.Time
		wantRetryAfter time.Duration
	}{
		{"403 X-RateLimit-Reset epoch", http.StatusForbidden, map[string]string{"X-RateLimit-Reset": "1780000000"}, ``, time.Unix(epoch, 0).UTC(), 0},
		{"429 Retry-After seconds", http.StatusTooManyRequests, map[string]string{"Retry-After": "60"}, ``, time.Time{}, 60 * time.Second},
		{"429 Retry-After HTTP-date", http.StatusTooManyRequests, map[string]string{"Retry-After": "Wed, 10 Jun 2026 00:15:00 GMT"}, ``, httpDate, 0},
		{"200 GraphQL RATE_LIMITED with headers", http.StatusOK, map[string]string{"X-RateLimit-Reset": "1780000000"}, `{"errors":[{"type":"RATE_LIMITED","message":"x"}]}`, time.Unix(epoch, 0).UTC(), 0},
		{"200 GraphQL RATE_LIMITED body fallback", http.StatusOK, nil, `{"data":{"rateLimit":{"remaining":0,"resetAt":"2026-06-10T00:15:00Z"}},"errors":[{"type":"RATE_LIMITED","message":"x"}]}`, httpDate, 0},
		{"403 no rate headers graceful", http.StatusForbidden, nil, ``, time.Time{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := jsonServerWithHeaders(t, tc.status, tc.headers, tc.body)
			_, err := fetcherTo(srv.URL, "tok").ListOpenIssues(context.Background(), "acme/widgets", 100)
			if !errors.Is(err, ErrRateLimited) {
				t.Fatalf("error = %v, want ErrRateLimited", err)
			}
			var rle RateLimitedError
			if !errors.As(err, &rle) {
				t.Fatalf("error %v is not a RateLimitedError", err)
			}
			if !rle.ResetAt.Equal(tc.wantResetAt) {
				t.Errorf("ResetAt = %v, want %v", rle.ResetAt, tc.wantResetAt)
			}
			if rle.RetryAfter != tc.wantRetryAfter {
				t.Errorf("RetryAfter = %v, want %v", rle.RetryAfter, tc.wantRetryAfter)
			}
		})
	}
}

// TestListOpenIssuesParsesRateLimit pins the success-path budget fact: (a) the
// query asks GitHub for the rateLimit field — a typo in the selection would
// silently ship and the budget would always read empty; (b) the returned budget
// decodes onto IssueListResult.RateLimit.
func TestListOpenIssuesParsesRateLimit(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		body := `{"data":{"rateLimit":{"remaining":4321,"resetAt":"2026-06-09T01:00:00Z"},"repository":{"issues":{
			"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[{"number":1,"createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]}}]
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
	if !strings.Contains(gotQuery, "rateLimit") {
		t.Errorf("query does not request rateLimit; got:\n%s", gotQuery)
	}
	if res.RateLimit == nil {
		t.Fatal("RateLimit = nil, want decoded budget")
	}
	if res.RateLimit.Remaining != 4321 {
		t.Errorf("Remaining = %d, want 4321", res.RateLimit.Remaining)
	}
	wantReset := time.Date(2026, 6, 9, 1, 0, 0, 0, time.UTC)
	if !res.RateLimit.ResetAt.Equal(wantReset) {
		t.Errorf("ResetAt = %v, want %v", res.RateLimit.ResetAt, wantReset)
	}
}

// TestListOpenIssuesRateLimitUsesLastPage pins that the budget reflects the most
// recent page — the freshest observation of remaining points — not the first.
func TestListOpenIssuesRateLimitUsesLastPage(t *testing.T) {
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
			body = `{"data":{"rateLimit":{"remaining":5000,"resetAt":"2026-06-09T01:00:00Z"},"repository":{"issues":{"totalCount":3,"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
				{"number":1,"createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]}},
				{"number":2,"createdAt":"2025-01-02T00:00:00Z","comments":{"nodes":[]}}
			]}}}}`
		} else {
			body = `{"data":{"rateLimit":{"remaining":4998,"resetAt":"2026-06-09T01:00:00Z"},"repository":{"issues":{"totalCount":3,"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"nodes":[
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
	if res.RateLimit == nil {
		t.Fatal("RateLimit = nil, want last page's budget")
	}
	if res.RateLimit.Remaining != 4998 {
		t.Errorf("Remaining = %d, want 4998 (last page wins)", res.RateLimit.Remaining)
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

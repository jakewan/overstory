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

// TestAuthoredSearchQueriesContainQualifiers is the guard the query-contract test
// cannot provide: the search qualifiers live inside the query *string* (passed as
// a variable), invisible to the selection-identifier drift check, so a dropped
// or typo'd is:pr/is:issue/author:/-author: would silently conflate or miscount
// with no other test failing. Each assembled string is pinned by its expected
// qualifier set, in the alias order (s0..s4) the query consumes.
func TestAuthoredSearchQueriesContainQualifiers(t *testing.T) {
	qs := authoredSearchQueries("acme/widgets", "alice", "2026-05-01T00:00:00Z", "2026-06-01T00:00:00Z")
	repo := "repo:acme/widgets"
	created := "created:2026-05-01T00:00:00Z..2026-06-01T00:00:00Z"
	updated := "updated:2026-05-01T00:00:00Z..2026-06-01T00:00:00Z"
	for _, tc := range []struct {
		name string
		got  string
		want []string
	}{
		{"issuesOpened", qs[0], []string{repo, "is:issue", "author:alice", created}},
		{"pullRequestsOpened", qs[1], []string{repo, "is:pr", "author:alice", created}},
		{"reviewsSubmitted", qs[2], []string{repo, "is:pr", "reviewed-by:alice", updated}},
		{"pullRequestsEngaged", qs[3], []string{repo, "is:pr", "commenter:alice", "-author:alice", updated}},
		{"issuesEngaged", qs[4], []string{repo, "is:issue", "commenter:alice", "-author:alice", updated}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, q := range tc.want {
				if !strings.Contains(tc.got, q) {
					t.Errorf("query %q missing qualifier %q", tc.got, q)
				}
			}
		})
	}
	// The opened categories must NOT carry the engagement exclusion, or they would
	// drop the author's own authored items.
	if strings.Contains(qs[0], "-author:") || strings.Contains(qs[1], "-author:") {
		t.Errorf("an opened-category query carries the -author exclusion: %q / %q", qs[0], qs[1])
	}
}

// authoredServer dispatches the two AuthoredActivity requests by inspecting the
// query body: the search/user request and the commit-history request hit the same
// endpoint, so the body's shape selects the canned response.
func authoredServer(t *testing.T, searchBody, historyBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		body := searchBody
		if strings.Contains(req.Query, "history(") {
			body = historyBody
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthoredActivityMapsSixCounts(t *testing.T) {
	search := `{"data":{"user":{"id":"U_1"},"s0":{"issueCount":3},"s1":{"issueCount":5},"s2":{"issueCount":7},"s3":{"issueCount":9},"s4":{"issueCount":4},"rateLimit":{"remaining":4990,"resetAt":"2026-06-20T00:00:00Z"}}}`
	history := `{"data":{"repository":{"defaultBranchRef":{"target":{"history":{"totalCount":12}}}},"rateLimit":{"remaining":4980,"resetAt":"2026-06-20T00:00:00Z"}}}`
	srv := authoredServer(t, search, history)

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	res, err := fetcherTo(srv.URL, "tok").AuthoredActivity(context.Background(), "acme/widgets", "alice", since, until)
	if err != nil {
		t.Fatalf("AuthoredActivity: %v", err)
	}
	// The alias order (s0..s4) maps to the six categories; pin it so a reorder is
	// caught (a mis-mapped count silently misleads the audit).
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"commitsAuthored", res.CommitsAuthored, 12},
		{"issuesOpened", res.IssuesOpened, 3},
		{"pullRequestsOpened", res.PullRequestsOpened, 5},
		{"reviewsSubmitted", res.ReviewsSubmitted, 7},
		{"pullRequestsEngaged", res.PullRequestsEngaged, 9},
		{"issuesEngaged", res.IssuesEngaged, 4},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	// The second (later) request's budget wins.
	if res.RateLimit == nil || res.RateLimit.Remaining != 4980 {
		t.Errorf("RateLimit = %+v, want the history request's budget (4980)", res.RateLimit)
	}
}

func TestAuthoredActivityUnknownAuthorErrors(t *testing.T) {
	// user resolves null — an unknown login, not a real-but-inactive user.
	search := `{"data":{"user":null,"s0":{"issueCount":0},"s1":{"issueCount":0},"s2":{"issueCount":0},"s3":{"issueCount":0},"s4":{"issueCount":0}}}`
	srv := authoredServer(t, search, "")

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	_, err := fetcherTo(srv.URL, "tok").AuthoredActivity(context.Background(), "acme/widgets", "nope", since, until)
	if !errors.Is(err, ErrAuthorNotFound) {
		t.Fatalf("err = %v, want ErrAuthorNotFound", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q does not name the unresolved login", err)
	}
}

func TestAuthoredActivityEmptyDefaultBranchCountsZeroCommits(t *testing.T) {
	search := `{"data":{"user":{"id":"U_1"},"s0":{"issueCount":1},"s1":{"issueCount":0},"s2":{"issueCount":0},"s3":{"issueCount":0},"s4":{"issueCount":0}}}`
	// A repo with no default branch (empty repo) returns null defaultBranchRef.
	history := `{"data":{"repository":{"defaultBranchRef":null}}}`
	srv := authoredServer(t, search, history)

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	res, err := fetcherTo(srv.URL, "tok").AuthoredActivity(context.Background(), "acme/widgets", "alice", since, until)
	if err != nil {
		t.Fatalf("AuthoredActivity: %v", err)
	}
	if res.CommitsAuthored != 0 {
		t.Errorf("CommitsAuthored = %d, want 0 for an empty default branch", res.CommitsAuthored)
	}
	if res.IssuesOpened != 1 {
		t.Errorf("IssuesOpened = %d, want 1 (a resolved user with a non-commit count)", res.IssuesOpened)
	}
}

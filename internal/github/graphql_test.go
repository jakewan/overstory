package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	// same-repo filtering applied. The first node's payload mirrors a real
	// CrossReferencedEvent capture (an Issue source alongside PullRequest sources),
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

func TestListOpenIssuesParsesNativeBlockedBy(t *testing.T) {
	// The fake-injected reduction specs set Issue.BlockedBy directly, so they never
	// exercise toIssue's wire→domain mapping — the enum-casing (state=="OPEN"), the
	// cross-repository drop, and the totalCount>nodes truncation arithmetic. This
	// drives that mapping against a fake GraphQL server: (a) the query must request
	// the blockedBy connection — a selection typo would silently ship and the
	// dependency signal would see no edges; (b) the edges decode onto
	// Issue.BlockedBy with the same-repo filter and open-state mapping applied,
	// preserving closed blockers (the open-only projection is the reduction's job).
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		// Issue 1: totalCount 4 but only 3 nodes returned → truncated. Same-repo #11
		// OPEN and #9 CLOSED are kept (closed retained at this layer); cross-repo #7
		// (other/repo) is dropped even though OPEN — its number would collide locally.
		// Issue 2: blockedBy is null. Issue 3: the field is absent entirely.
		body := `{"data":{"repository":{"issues":{
			"totalCount":3,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"title":"a","url":"ua","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "blockedBy":{"totalCount":4,"nodes":[
					{"number":11,"state":"OPEN","repository":{"nameWithOwner":"acme/widgets"}},
					{"number":9,"state":"CLOSED","repository":{"nameWithOwner":"acme/widgets"}},
					{"number":7,"state":"OPEN","repository":{"nameWithOwner":"other/repo"}}
				 ]}},
				{"number":2,"title":"b","url":"ub","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "blockedBy":null},
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
	if !strings.Contains(gotQuery, "blockedBy") {
		t.Errorf("query does not request the native blockedBy connection; got:\n%s", gotQuery)
	}
	if len(res.Issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(res.Issues))
	}

	// Issue 1: cross-repo #7 dropped; same-repo #11 (open) and #9 (closed) kept,
	// order preserved, open state mapped from the enum.
	want := []DependencyRef{{Number: 11, Open: true}, {Number: 9, Open: false}}
	if got := res.Issues[0].BlockedBy; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("issue 1 BlockedBy = %v, want %v", got, want)
	}
	if !res.Issues[0].BlockedByTruncated {
		t.Error("issue 1 BlockedByTruncated = false, want true (totalCount 4 > 3 nodes)")
	}
	// Issues 2 (null) and 3 (absent): no blockers, not truncated, no panic.
	for _, i := range []int{1, 2} {
		if got := res.Issues[i].BlockedBy; len(got) != 0 {
			t.Errorf("issue %d BlockedBy = %v, want empty", res.Issues[i].Number, got)
		}
		if res.Issues[i].BlockedByTruncated {
			t.Errorf("issue %d BlockedByTruncated = true, want false", res.Issues[i].Number)
		}
	}
}

func TestListOpenIssuesParsesNativeBlocking(t *testing.T) {
	// The reverse-direction mirror of TestListOpenIssuesParsesNativeBlockedBy, and the
	// sole real guard for the blocking sub-selection: the query-decode contract test
	// flattens the whole query into one token set, so the sibling blockedBy block
	// already supplies state/repository/nameWithOwner — a blocking block that dropped
	// those would pass it green. Only a wire-decode test exercising blocking's own
	// edges catches it. (a) the query must request the blocking connection; (b) the
	// edges decode onto Issue.Blocking with the same-repo filter and open-state mapping
	// applied, preserving closed edges (the open-only projection is the reduction's
	// job); (c) a totalCount exceeding the nodes sets BlockingTruncated.
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		// Issue 1: totalCount 4 but only 3 nodes returned → truncated. Same-repo #21
		// OPEN and #19 CLOSED are kept (closed retained at this layer); cross-repo #17
		// (other/repo) is dropped even though OPEN — its number would collide locally.
		// Issue 2: blocking is null. Issue 3: the field is absent entirely.
		body := `{"data":{"repository":{"issues":{
			"totalCount":3,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"title":"a","url":"ua","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "blocking":{"totalCount":4,"nodes":[
					{"number":21,"state":"OPEN","repository":{"nameWithOwner":"acme/widgets"}},
					{"number":19,"state":"CLOSED","repository":{"nameWithOwner":"acme/widgets"}},
					{"number":17,"state":"OPEN","repository":{"nameWithOwner":"other/repo"}}
				 ]}},
				{"number":2,"title":"b","url":"ub","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "blocking":null},
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
	if !strings.Contains(gotQuery, "blocking") {
		t.Errorf("query does not request the native blocking connection; got:\n%s", gotQuery)
	}
	if len(res.Issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(res.Issues))
	}

	// Issue 1: cross-repo #17 dropped; same-repo #21 (open) and #19 (closed) kept,
	// order preserved, open state mapped from the enum.
	want := []DependencyRef{{Number: 21, Open: true}, {Number: 19, Open: false}}
	if got := res.Issues[0].Blocking; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("issue 1 Blocking = %v, want %v", got, want)
	}
	if !res.Issues[0].BlockingTruncated {
		t.Error("issue 1 BlockingTruncated = false, want true (totalCount 4 > 3 nodes)")
	}
	// Issues 2 (null) and 3 (absent): no blocking edges, not truncated, no panic.
	for _, i := range []int{1, 2} {
		if got := res.Issues[i].Blocking; len(got) != 0 {
			t.Errorf("issue %d Blocking = %v, want empty", res.Issues[i].Number, got)
		}
		if res.Issues[i].BlockingTruncated {
			t.Errorf("issue %d BlockingTruncated = true, want false", res.Issues[i].Number)
		}
	}
}

func TestListOpenIssuesParsesNativeSubIssues(t *testing.T) {
	// The sub-issue hierarchy mirror of the blocking test, and the sole real guard for
	// the subIssues sub-selection: the query-decode contract test flattens the whole
	// query into one token set, so the sibling blockedBy/blocking blocks already supply
	// state/repository/nameWithOwner — a subIssues block that dropped one would pass it
	// green. Only a wire-decode test exercising subIssues' own edges catches it.
	// (a) the query must request the subIssues connection and the subIssuesSummary; (b)
	// the edges decode onto Issue.SubIssues with the same-repo filter and open-state
	// mapping applied, preserving closed children (the open-only projection is the
	// reduction's job); (c) a totalCount exceeding the nodes sets SubIssuesTruncated;
	// (d) the summary's total/completed decode independently of the connection (the
	// untruncated authoritative counts), with a null summary reading as 0/0.
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		// Issue 1: subIssues totalCount 4 but only 3 nodes returned → truncated. Same-repo
		// #21 OPEN and #19 CLOSED are kept (closed retained at this layer); cross-repo #17
		// (other/repo) is dropped even though OPEN — its number would collide locally. The
		// summary (total 5, completed 2) is the untruncated authoritative pair and decodes
		// independently. Issue 2: subIssues and summary are null. Issue 3: fields absent.
		body := `{"data":{"repository":{"issues":{
			"totalCount":3,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"title":"a","url":"ua","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "subIssues":{"totalCount":4,"nodes":[
					{"number":21,"state":"OPEN","repository":{"nameWithOwner":"acme/widgets"}},
					{"number":19,"state":"CLOSED","repository":{"nameWithOwner":"acme/widgets"}},
					{"number":17,"state":"OPEN","repository":{"nameWithOwner":"other/repo"}}
				 ]},
				 "subIssuesSummary":{"total":5,"completed":2}},
				{"number":2,"title":"b","url":"ub","createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]},
				 "subIssues":null,"subIssuesSummary":null},
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
	if !strings.Contains(gotQuery, "subIssues") {
		t.Errorf("query does not request the native subIssues connection; got:\n%s", gotQuery)
	}
	if !strings.Contains(gotQuery, "subIssuesSummary") {
		t.Errorf("query does not request subIssuesSummary; got:\n%s", gotQuery)
	}
	if len(res.Issues) != 3 {
		t.Fatalf("got %d issues, want 3", len(res.Issues))
	}

	// Issue 1: cross-repo #17 dropped; same-repo #21 (open) and #19 (closed) kept,
	// order preserved, open state mapped from the enum.
	want := []DependencyRef{{Number: 21, Open: true}, {Number: 19, Open: false}}
	if got := res.Issues[0].SubIssues; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("issue 1 SubIssues = %v, want %v", got, want)
	}
	if !res.Issues[0].SubIssuesTruncated {
		t.Error("issue 1 SubIssuesTruncated = false, want true (totalCount 4 > 3 nodes)")
	}
	// The summary is independent of the connection and untruncated.
	if res.Issues[0].SubIssuesTotal != 5 || res.Issues[0].SubIssuesCompleted != 2 {
		t.Errorf("issue 1 summary = %d/%d, want 5/2 (total/completed)",
			res.Issues[0].SubIssuesTotal, res.Issues[0].SubIssuesCompleted)
	}
	// Issues 2 (null) and 3 (absent): no children, not truncated, summary 0/0, no panic.
	for _, i := range []int{1, 2} {
		if got := res.Issues[i].SubIssues; len(got) != 0 {
			t.Errorf("issue %d SubIssues = %v, want empty", res.Issues[i].Number, got)
		}
		if res.Issues[i].SubIssuesTruncated {
			t.Errorf("issue %d SubIssuesTruncated = true, want false", res.Issues[i].Number)
		}
		if res.Issues[i].SubIssuesTotal != 0 || res.Issues[i].SubIssuesCompleted != 0 {
			t.Errorf("issue %d summary = %d/%d, want 0/0 (total/completed)", res.Issues[i].Number,
				res.Issues[i].SubIssuesTotal, res.Issues[i].SubIssuesCompleted)
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

// pagedIssueServer serves a repo of totalCount open issues as pages, deriving the
// page start from the cursor and honoring the fetch's requested first — so a test can
// exercise both a full paginate-to-completion and a fetchLimit-bounded truncation
// against a window larger than a single page.
func pagedIssueServer(t *testing.T, totalCount int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				First int     `json:"first"`
				After *string `json:"after"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		start := 0
		if req.Variables.After != nil {
			if _, err := fmt.Sscanf(*req.Variables.After, "cursor-%d", &start); err != nil {
				t.Errorf("parse cursor %q: %v", *req.Variables.After, err)
			}
		}
		end := min(start+req.Variables.First, totalCount)
		nodes := make([]string, 0, end-start)
		for n := start + 1; n <= end; n++ {
			nodes = append(nodes, fmt.Sprintf(`{"number":%d,"createdAt":"2025-01-01T00:00:00Z","comments":{"nodes":[]}}`, n))
		}
		body := fmt.Sprintf(`{"data":{"repository":{"issues":{"totalCount":%d,"pageInfo":{"hasNextPage":%t,"endCursor":"cursor-%d"},"nodes":[%s]}}}}`,
			totalCount, end < totalCount, end, strings.Join(nodes, ","))
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
}

// TestListOpenIssuesPaginatesToCompletion pins that when the repo's open count
// exceeds a single page but stays under the fetchLimit backstop, the fetch collects
// the entire open set — no issue is dropped, so the newest issues (past the old
// 200 cap) are present and the window is not truncated.
func TestListOpenIssuesPaginatesToCompletion(t *testing.T) {
	srv := pagedIssueServer(t, 250)
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListOpenIssues(context.Background(), "acme/widgets", 2000)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(res.Issues) != 250 || res.TotalOpen != 250 {
		t.Fatalf("fetched %d of TotalOpen %d, want 250/250 (full window)", len(res.Issues), res.TotalOpen)
	}
	// The last issue (highest number) is the newest by the seeded order — it must be
	// present, since the newest-drop bug is the whole point of the change.
	if res.Issues[len(res.Issues)-1].Number != 250 {
		t.Errorf("last fetched issue = #%d, want #250 (newest present)", res.Issues[len(res.Issues)-1].Number)
	}
}

// TestListOpenIssuesBackstopTruncates pins the backstop: when the open count
// exceeds the fetchLimit, the fetch stops at the limit and still reports the exact
// TotalOpen, so a downstream reduction's fetchTruncated (len < TotalOpen) fires.
func TestListOpenIssuesBackstopTruncates(t *testing.T) {
	srv := pagedIssueServer(t, 250)
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListOpenIssues(context.Background(), "acme/widgets", 150)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(res.Issues) != 150 || res.TotalOpen != 250 {
		t.Errorf("fetched %d of TotalOpen %d, want 150/250 (backstop hit, count exact)", len(res.Issues), res.TotalOpen)
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
		// A top-level error returns a literal `data: null`. The doRaw rateLimit peek
		// must not mistake unmarshalling that null for a failure and mask the real
		// GraphQL error before classification runs.
		{"data null with error", http.StatusOK, `{"data":null,"errors":[{"type":"NOT_FOUND","message":"x"}]}`, ErrRepoNotFound},
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

// TestListIssuesUpdatedSinceParsesActivity pins the activity fetch: (a) the query
// asks GitHub for both states and closedAt — a typo would silently ship and the
// trajectory would misread closures; (b) timestamps decode onto IssueActivity,
// with a null closedAt reading as the zero time (open); (c) the budget threads
// through; (d) an exhausted connection (no more pages) is complete, not truncated.
func TestListIssuesUpdatedSinceParsesActivity(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		body := `{"data":{"rateLimit":{"remaining":4990,"resetAt":"2026-06-09T01:00:00Z"},"repository":{"issues":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"createdAt":"2026-02-01T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"},
				{"number":2,"createdAt":"2026-01-01T00:00:00Z","closedAt":"2026-04-01T00:00:00Z","updatedAt":"2026-04-01T00:00:00Z"}
			]
		}}}}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListIssuesUpdatedSince(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssuesUpdatedSince: %v", err)
	}
	if !strings.Contains(gotQuery, "closedAt") || !strings.Contains(gotQuery, "CLOSED") {
		t.Errorf("query does not request closed-issue activity; got:\n%s", gotQuery)
	}
	if len(res.Activities) != 2 {
		t.Fatalf("got %d activities, want 2", len(res.Activities))
	}
	if !res.Activities[0].ClosedAt.IsZero() {
		t.Errorf("issue 1 ClosedAt = %v, want zero (null → open)", res.Activities[0].ClosedAt)
	}
	wantClosed := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !res.Activities[1].ClosedAt.Equal(wantClosed) {
		t.Errorf("issue 2 ClosedAt = %v, want %v", res.Activities[1].ClosedAt, wantClosed)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false (connection exhausted, fully covered)")
	}
	if res.RateLimit == nil || res.RateLimit.Remaining != 4990 {
		t.Errorf("RateLimit = %+v, want remaining 4990", res.RateLimit)
	}
}

// TestListIssuesUpdatedSinceStopsAtFloor pins the floor-stop: once a node updated
// before `since` appears (DESC order), it and everything after are out of window,
// so the scan stops and excludes it — and the result is complete (not truncated)
// even though the page reported more pages, because the floor proves coverage.
func TestListIssuesUpdatedSinceStopsAtFloor(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	body := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":true,"endCursor":"c1"},
		"nodes":[
			{"number":1,"createdAt":"2026-04-15T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"},
			{"number":2,"createdAt":"2026-01-01T00:00:00Z","closedAt":null,"updatedAt":"2026-02-01T00:00:00Z"}
		]
	}}}}`
	srv := jsonServer(t, http.StatusOK, body)
	res, err := fetcherTo(srv.URL, "tok").ListIssuesUpdatedSince(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssuesUpdatedSince: %v", err)
	}
	if len(res.Activities) != 1 || res.Activities[0].Number != 1 {
		t.Errorf("Activities = %+v, want only issue 1 (issue 2 is past the floor)", res.Activities)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false (floor crossed proves coverage despite hasNextPage)")
	}
}

// TestListIssuesUpdatedSinceTruncatesAtFetchLimit pins the never-silently-truncate
// contract: when the fetch cap is reached before the floor is crossed, coverage is
// unproven, so the result is truncated and the trajectory counts are lower bounds.
func TestListIssuesUpdatedSinceTruncatesAtFetchLimit(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	body := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":true,"endCursor":"c1"},
		"nodes":[
			{"number":1,"createdAt":"2026-04-15T00:00:00Z","closedAt":null,"updatedAt":"2026-05-02T00:00:00Z"},
			{"number":2,"createdAt":"2026-04-10T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"}
		]
	}}}}`
	srv := jsonServer(t, http.StatusOK, body)
	res, err := fetcherTo(srv.URL, "tok").ListIssuesUpdatedSince(context.Background(), "acme/widgets", since, 2)
	if err != nil {
		t.Fatalf("ListIssuesUpdatedSince: %v", err)
	}
	if len(res.Activities) != 2 {
		t.Fatalf("got %d activities, want 2 (the cap)", len(res.Activities))
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true (fetch cap hit before the floor)")
	}
}

// TestListIssuesUpdatedSinceTruncatesOnUnusableCursor pins that a page reporting
// more pages (hasNextPage) but no cursor to fetch them is treated as unproven
// coverage — truncated — not as exhaustion. Claiming exhaustion there would
// report lower-bound counts as complete, the silent-truncation the contract bars.
func TestListIssuesUpdatedSinceTruncatesOnUnusableCursor(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	body := `{"data":{"repository":{"issues":{
		"pageInfo":{"hasNextPage":true,"endCursor":""},
		"nodes":[
			{"number":1,"createdAt":"2026-04-15T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"}
		]
	}}}}`
	srv := jsonServer(t, http.StatusOK, body)
	res, err := fetcherTo(srv.URL, "tok").ListIssuesUpdatedSince(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssuesUpdatedSince: %v", err)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true (hasNextPage with no cursor is unproven coverage)")
	}
}

// TestListPullRequestsUpdatedSinceParsesActivity mirrors the issue-activity parse
// test for the PR-trajectory fetch, and additionally pins the one branch unique to
// PRs: a MERGED-state node carries a non-null closedAt, so merged outflow decodes
// and is captured the same as a closed-without-merge PR.
func TestListPullRequestsUpdatedSinceParsesActivity(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		body := `{"data":{"rateLimit":{"remaining":4980,"resetAt":"2026-06-09T01:00:00Z"},"repository":{"pullRequests":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"createdAt":"2026-02-01T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"},
				{"number":2,"createdAt":"2026-01-01T00:00:00Z","closedAt":"2026-04-01T00:00:00Z","updatedAt":"2026-04-01T00:00:00Z"}
			]
		}}}}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListPullRequestsUpdatedSince(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListPullRequestsUpdatedSince: %v", err)
	}
	// The MERGED state is what captures merged outflow (a merged PR is MERGED, not
	// CLOSED); closedAt is what the reduction counts as closed.
	if !strings.Contains(gotQuery, "closedAt") || !strings.Contains(gotQuery, "MERGED") {
		t.Errorf("query does not request open-and-closed/merged PR activity; got:\n%s", gotQuery)
	}
	if len(res.Activities) != 2 {
		t.Fatalf("got %d activities, want 2", len(res.Activities))
	}
	if !res.Activities[0].ClosedAt.IsZero() {
		t.Errorf("PR 1 ClosedAt = %v, want zero (null → open)", res.Activities[0].ClosedAt)
	}
	wantClosed := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !res.Activities[1].ClosedAt.Equal(wantClosed) {
		t.Errorf("PR 2 ClosedAt = %v, want %v (merged/closed outflow)", res.Activities[1].ClosedAt, wantClosed)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false (connection exhausted, fully covered)")
	}
	if res.RateLimit == nil || res.RateLimit.Remaining != 4980 {
		t.Errorf("RateLimit = %+v, want remaining 4980", res.RateLimit)
	}
}

// TestListPullRequestsUpdatedSinceStopsAtFloor mirrors the issue twin: once a PR
// updated before `since` appears (DESC order), it and everything after are out of
// window, so the scan stops and the result is complete (not truncated) even though
// the page reported more pages.
func TestListPullRequestsUpdatedSinceStopsAtFloor(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	body := `{"data":{"repository":{"pullRequests":{
		"pageInfo":{"hasNextPage":true,"endCursor":"c1"},
		"nodes":[
			{"number":1,"createdAt":"2026-04-15T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"},
			{"number":2,"createdAt":"2026-01-01T00:00:00Z","closedAt":null,"updatedAt":"2026-02-01T00:00:00Z"}
		]
	}}}}`
	srv := jsonServer(t, http.StatusOK, body)
	res, err := fetcherTo(srv.URL, "tok").ListPullRequestsUpdatedSince(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListPullRequestsUpdatedSince: %v", err)
	}
	if len(res.Activities) != 1 || res.Activities[0].Number != 1 {
		t.Errorf("Activities = %+v, want only PR 1 (PR 2 is past the floor)", res.Activities)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false (floor crossed proves coverage despite hasNextPage)")
	}
}

// TestListPullRequestsUpdatedSinceTruncatesAtFetchLimit pins the never-silently-
// truncate contract for the PR fetch: the cap reached before the floor is crossed
// leaves coverage unproven, so the counts are lower bounds.
func TestListPullRequestsUpdatedSinceTruncatesAtFetchLimit(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	body := `{"data":{"repository":{"pullRequests":{
		"pageInfo":{"hasNextPage":true,"endCursor":"c1"},
		"nodes":[
			{"number":1,"createdAt":"2026-04-15T00:00:00Z","closedAt":null,"updatedAt":"2026-05-02T00:00:00Z"},
			{"number":2,"createdAt":"2026-04-10T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"}
		]
	}}}}`
	srv := jsonServer(t, http.StatusOK, body)
	res, err := fetcherTo(srv.URL, "tok").ListPullRequestsUpdatedSince(context.Background(), "acme/widgets", since, 2)
	if err != nil {
		t.Fatalf("ListPullRequestsUpdatedSince: %v", err)
	}
	if len(res.Activities) != 2 {
		t.Fatalf("got %d activities, want 2 (the cap)", len(res.Activities))
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true (fetch cap hit before the floor)")
	}
}

// TestListPullRequestsUpdatedSinceTruncatesOnUnusableCursor pins that a page
// reporting more pages but no cursor to fetch them is unproven coverage —
// truncated — not exhaustion, for the PR fetch.
func TestListPullRequestsUpdatedSinceTruncatesOnUnusableCursor(t *testing.T) {
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	body := `{"data":{"repository":{"pullRequests":{
		"pageInfo":{"hasNextPage":true,"endCursor":""},
		"nodes":[
			{"number":1,"createdAt":"2026-04-15T00:00:00Z","closedAt":null,"updatedAt":"2026-05-01T00:00:00Z"}
		]
	}}}}`
	srv := jsonServer(t, http.StatusOK, body)
	res, err := fetcherTo(srv.URL, "tok").ListPullRequestsUpdatedSince(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListPullRequestsUpdatedSince: %v", err)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true (hasNextPage with no cursor is unproven coverage)")
	}
}

// restFetcherTo builds a fetcher whose REST base points at a test server — the
// GraphQL endpoint is left unused, since the issue-events fetch is REST-only.
func restFetcherTo(url, token string) *GraphQLFetcher {
	return &GraphQLFetcher{restEndpoint: url, tokens: staticToken{token: token}, client: &http.Client{}}
}

// writeBody writes a response body from a multi-page test handler, failing the
// test on a write error (errcheck's check-blank bars discarding it to _).
func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

// eventsBudgetHeaders is the REST core-pool budget headers a real events response
// carries, so the budget-decode and rate-signal cases share one fixture.
func eventsBudgetHeaders(remaining string, resetEpoch int64) map[string]string {
	return map[string]string{
		"X-RateLimit-Remaining": remaining,
		"X-RateLimit-Reset":     strconv.FormatInt(resetEpoch, 10),
		"Content-Type":          "application/json",
	}
}

// TestListIssueEventsDecodesPayloadPerType pins the REST decode: a bare JSON array
// of mixed event types flattens into IssueEvent with each type's payload mapped,
// the actor/issue/PR fields populated, and performed_via_github_app read as the
// automation flag.
func TestListIssueEventsDecodesPayloadPerType(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	body := `[
		{"id":10,"event":"labeled","created_at":"2026-06-10T00:00:00Z","actor":{"login":"jakewan"},
		 "issue":{"number":100,"title":"an issue","pull_request":null},"label":{"name":"reductions"},"performed_via_github_app":null},
		{"id":11,"event":"milestoned","created_at":"2026-06-10T01:00:00Z","actor":{"login":"jakewan"},
		 "issue":{"number":100,"title":"an issue"},"milestone":{"title":"Round 6"}},
		{"id":12,"event":"assigned","created_at":"2026-06-10T02:00:00Z","actor":{"login":"jakewan"},
		 "issue":{"number":100,"title":"an issue"},"assignee":{"login":"jakewan"}},
		{"id":13,"event":"renamed","created_at":"2026-06-10T03:00:00Z","actor":{"login":"jakewan"},
		 "issue":{"number":100,"title":"an issue"},"rename":{"from":"old","to":"new"},"performed_via_github_app":{"id":42}},
		{"id":14,"event":"closed","created_at":"2026-06-10T04:00:00Z","actor":{"login":"jakewan"},
		 "issue":{"number":200,"title":"a pr","pull_request":{"url":"u"}}}
	]`
	srv := jsonServerWithHeaders(t, http.StatusOK, eventsBudgetHeaders("4990", 1750000000), body)
	res, err := restFetcherTo(srv.URL, "tok").ListIssueEvents(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	if len(res.Events) != 5 {
		t.Fatalf("len(Events) = %d, want 5", len(res.Events))
	}
	byID := map[int64]IssueEvent{}
	for _, e := range res.Events {
		byID[e.EventID] = e
	}
	if e := byID[10]; e.Type != "labeled" || e.Label != "reductions" || e.Actor != "jakewan" || e.IssueNumber != 100 || e.IssueIsPR || e.ViaAutomation {
		t.Errorf("labeled event = %+v", e)
	}
	if e := byID[11]; e.Milestone != "Round 6" {
		t.Errorf("milestoned.Milestone = %q, want Round 6", e.Milestone)
	}
	if e := byID[12]; e.Assignee != "jakewan" {
		t.Errorf("assigned.Assignee = %q, want jakewan", e.Assignee)
	}
	if e := byID[13]; e.RenameFrom != "old" || e.RenameTo != "new" || !e.ViaAutomation {
		t.Errorf("renamed event = %+v, want from/to set and ViaAutomation true", e)
	}
	if e := byID[14]; !e.IssueIsPR {
		t.Errorf("closed PR event IsPullRequest = false, want true")
	}
	// Header-only budget decodes.
	if res.RateLimit == nil || res.RateLimit.Remaining != 4990 {
		t.Errorf("RateLimit = %+v, want Remaining 4990", res.RateLimit)
	}
}

// TestListIssueEventsNullActor pins that a null actor (a deleted user) decodes to
// an empty Actor without panicking, so it simply won't match a measured login.
func TestListIssueEventsNullActor(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	body := `[{"id":1,"event":"closed","created_at":"2026-06-10T00:00:00Z","actor":null,"issue":{"number":1,"title":"x"}}]`
	srv := jsonServerWithHeaders(t, http.StatusOK, eventsBudgetHeaders("5000", 1750000000), body)
	res, err := restFetcherTo(srv.URL, "tok").ListIssueEvents(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Actor != "" {
		t.Errorf("events = %+v, want one event with empty actor", res.Events)
	}
}

// TestListIssueEventsFloorCrossingStopsAndProvesCoverage pins multi-page
// pagination via the Link header and the floor-crossing stop: page 1 is all
// in-window with a rel="next", page 2 carries an event older than `since`, so the
// scan crosses the floor and reports Truncated false (coverage proven).
func TestListIssueEventsFloorCrossingStopsAndProvesCoverage(t *testing.T) {
	since := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range eventsBudgetHeaders("4000", 1750000000) {
			w.Header().Set(k, v)
		}
		page := r.URL.Query().Get("page")
		if page == "" || page == "1" {
			w.Header().Set("Link", "<http://"+r.Host+r.URL.Path+"?page=2>; rel=\"next\"")
			w.WriteHeader(http.StatusOK)
			writeBody(t, w, `[{"id":20,"event":"labeled","created_at":"2026-06-10T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}}]`)
			return
		}
		// Page 2: an event before the floor — the scan must stop here.
		w.WriteHeader(http.StatusOK)
		writeBody(t, w, `[{"id":10,"event":"labeled","created_at":"2026-06-01T00:00:00Z","actor":{"login":"a"},"issue":{"number":2,"title":"y"}}]`)
	}))
	t.Cleanup(srv.Close)

	res, err := restFetcherTo(srv.URL, "tok").ListIssueEvents(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	// Only the in-window event survives; the pre-floor one is dropped.
	if len(res.Events) != 1 || res.Events[0].EventID != 20 {
		t.Fatalf("events = %+v, want only id 20 (in-window)", res.Events)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false (the floor was crossed)")
	}
}

// TestListIssueEventsDedupesAcrossPages pins cross-page dedup by event id: a write
// that shifts the offset window can resurface an event on the next page, and the
// monotonic id is what keeps it from being double-counted.
func TestListIssueEventsDedupesAcrossPages(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range eventsBudgetHeaders("4000", 1750000000) {
			w.Header().Set(k, v)
		}
		if p := r.URL.Query().Get("page"); p == "" || p == "1" {
			w.Header().Set("Link", "<http://"+r.Host+r.URL.Path+"?page=2>; rel=\"next\"")
			w.WriteHeader(http.StatusOK)
			writeBody(t, w, `[
				{"id":30,"event":"labeled","created_at":"2026-06-10T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}},
				{"id":29,"event":"labeled","created_at":"2026-06-09T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}}
			]`)
			return
		}
		// Page 2 repeats id 29 (offset shift) then a fresh id 28; no Link → exhausted.
		w.WriteHeader(http.StatusOK)
		writeBody(t, w, `[
			{"id":29,"event":"labeled","created_at":"2026-06-09T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}},
			{"id":28,"event":"labeled","created_at":"2026-06-08T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}}
		]`)
	}))
	t.Cleanup(srv.Close)

	res, err := restFetcherTo(srv.URL, "tok").ListIssueEvents(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	if len(res.Events) != 3 {
		t.Fatalf("len(Events) = %d, want 3 (id 29 deduped across pages)", len(res.Events))
	}
	if res.Truncated {
		t.Error("Truncated = true, want false (stream exhausted, no rel=next)")
	}
}

// TestListIssueEventsTruncatesOnFetchCap pins the cap-driven truncation: when the
// fetch limit is reached before the floor is crossed or the stream drained,
// Truncated is true so the lower-bound coverage is never reported as complete.
func TestListIssueEventsTruncatesOnFetchCap(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	srv := jsonServerWithHeaders(t, http.StatusOK, map[string]string{
		"X-RateLimit-Remaining": "4000", "X-RateLimit-Reset": "1750000000",
		"Link": "", // set below per request is unnecessary; a present Link keeps coverage unproven
	}, `[
		{"id":2,"event":"labeled","created_at":"2026-06-10T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}},
		{"id":1,"event":"labeled","created_at":"2026-06-09T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}}
	]`)
	res, err := restFetcherTo(srv.URL, "tok").ListIssueEvents(context.Background(), "acme/widgets", since, 1)
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("len(Events) = %d, want 1 (capped at fetchLimit)", len(res.Events))
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true (fetch cap hit before coverage proven)")
	}
}

// TestListIssueEventsBudgetRequiresRemaining pins that a response carrying a reset
// header but no remaining count yields a nil budget rather than a fabricated
// Remaining 0 — the aggregator must never read an unknown budget as "0 left".
func TestListIssueEventsBudgetRequiresRemaining(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	srv := jsonServerWithHeaders(t, http.StatusOK, map[string]string{
		"X-RateLimit-Reset": "1750000000", // reset present, remaining absent
		"Content-Type":      "application/json",
	}, `[{"id":1,"event":"labeled","created_at":"2026-06-10T00:00:00Z","actor":{"login":"a"},"issue":{"number":1,"title":"x"}}]`)
	res, err := restFetcherTo(srv.URL, "tok").ListIssueEvents(context.Background(), "acme/widgets", since, 100)
	if err != nil {
		t.Fatalf("ListIssueEvents: %v", err)
	}
	if res.RateLimit != nil {
		t.Errorf("RateLimit = %+v, want nil (no remaining count means no usable budget)", res.RateLimit)
	}
}

// TestListIssueEventsClassifiesStatus pins the REST-specific status mapping that
// deliberately differs from the GraphQL classifier: a 404 and a 403 without any
// rate signal both surface as ErrRepoNotFound (never a RateLimitedError that would
// trip batch backpressure), while a 429 and a 403 carrying a rate signal surface
// as ErrRateLimited.
func TestListIssueEventsClassifiesStatus(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		status      int
		headers     map[string]string
		body        string
		wantNotFnd  bool
		wantLimited bool
	}{
		{"404 not found", http.StatusNotFound, nil, `{"message":"Not Found"}`, true, false},
		{"403 no rate signal is access error", http.StatusForbidden, nil, `{"message":"Must have admin rights"}`, true, false},
		{"403 prose mentioning rate limit is not a throttle", http.StatusForbidden, nil, `{"message":"Resource not accessible; see the rate limit docs"}`, true, false},
		{"403 with depleted remaining is rate limited", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1750000000"}, `{"message":"API rate limit exceeded"}`, false, true},
		{"403 secondary-limit body is rate limited", http.StatusForbidden, nil, `{"message":"You have exceeded a secondary rate limit"}`, false, true},
		{"429 is always rate limited", http.StatusTooManyRequests, nil, `{"message":"Too Many Requests"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := jsonServerWithHeaders(t, tc.status, tc.headers, tc.body)
			_, err := restFetcherTo(srv.URL, "tok").ListIssueEvents(context.Background(), "acme/widgets", since, 100)
			if err == nil {
				t.Fatal("err = nil, want a classified error")
			}
			if got := errors.Is(err, ErrRepoNotFound); got != tc.wantNotFnd {
				t.Errorf("errors.Is(ErrRepoNotFound) = %v, want %v (err: %v)", got, tc.wantNotFnd, err)
			}
			if got := errors.Is(err, ErrRateLimited); got != tc.wantLimited {
				t.Errorf("errors.Is(ErrRateLimited) = %v, want %v (err: %v)", got, tc.wantLimited, err)
			}
		})
	}
}

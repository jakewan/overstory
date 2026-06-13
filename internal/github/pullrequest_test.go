package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestListOpenPullRequestsParses asserts the query asks GitHub for pull requests
// and the head commit's check rollup, and that the nodes decode — including the
// two CI cases the orientation reduction distinguishes: a populated rollup
// (CIStatus is the state) and a null rollup (CIStatus empty, "no rollup"). The
// fixture covers both in one round-trip so a decode that mishandled the null
// pointer would surface here.
func TestListOpenPullRequestsParses(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		body := `{"data":{"repository":{"pullRequests":{
			"totalCount":2,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":10,"title":"feat x","url":"u10","isDraft":false,
				 "createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-02-01T00:00:00Z",
				 "headRefName":"feature/x",
				 "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}},
				{"number":11,"title":"wip y","url":"u11","isDraft":true,
				 "createdAt":"2025-01-03T00:00:00Z","updatedAt":"2025-01-04T00:00:00Z",
				 "headRefName":"feature/y",
				 "commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}
			]
		}}}}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListOpenPullRequests(context.Background(), "acme/widgets", 100)
	if err != nil {
		t.Fatalf("ListOpenPullRequests: %v", err)
	}
	if !strings.Contains(gotQuery, "pullRequests(") {
		t.Errorf("query does not request pullRequests; got:\n%s", gotQuery)
	}
	if !strings.Contains(gotQuery, "statusCheckRollup{") {
		t.Errorf("query does not request statusCheckRollup; got:\n%s", gotQuery)
	}
	if res.TotalOpen != 2 || len(res.PullRequests) != 2 {
		t.Fatalf("TotalOpen=%d len=%d, want 2/2", res.TotalOpen, len(res.PullRequests))
	}
	ready := res.PullRequests[0]
	if ready.Number != 10 || ready.IsDraft || ready.HeadRefName != "feature/x" || ready.CIStatus != "SUCCESS" {
		t.Errorf("PR 10 = %+v, want ready feature/x SUCCESS", ready)
	}
	draft := res.PullRequests[1]
	if draft.Number != 11 || !draft.IsDraft || draft.CIStatus != "" {
		t.Errorf("PR 11 = %+v, want draft with empty CIStatus (null rollup)", draft)
	}
}

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

// TestListOpenMilestonesParsesCounts asserts two things in one round-trip: the
// query asks GitHub for milestones, and the three counts decode onto distinct
// fields. The fixture deliberately gives the connection total (2), the open count
// (5), and the closed count (3) three different values: the struct↔query contract
// guard is nesting-blind (it only checks each json tag appears somewhere), so a
// collision where open/closed/total decode from the wrong totalCount would pass
// the guard — only asserting the distinct values here catches it.
func TestListOpenMilestonesParsesCounts(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		body := `{"data":{"repository":{"milestones":{
			"totalCount":2,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":7,"title":"Round 5","url":"u7",
				 "open":{"totalCount":5},
				 "closed":{"totalCount":3}}
			]
		}}}}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListOpenMilestones(context.Background(), "acme/widgets", 100)
	if err != nil {
		t.Fatalf("ListOpenMilestones: %v", err)
	}
	if !strings.Contains(gotQuery, "milestones(") {
		t.Errorf("query does not request milestones; got:\n%s", gotQuery)
	}
	if res.TotalOpen != 2 {
		t.Errorf("TotalOpen = %d, want 2 (the connection's own total)", res.TotalOpen)
	}
	if len(res.Milestones) != 1 {
		t.Fatalf("got %d milestones, want 1", len(res.Milestones))
	}
	m := res.Milestones[0]
	if m.Number != 7 || m.Title != "Round 5" || m.URL != "u7" {
		t.Errorf("milestone identity = %+v, want {7 Round 5 u7 …}", m)
	}
	if m.OpenIssues != 5 || m.ClosedIssues != 3 {
		t.Errorf("counts = open %d / closed %d, want 5/3 (distinct from total 2 — no collision)", m.OpenIssues, m.ClosedIssues)
	}
}

// TestListOpenMilestonesParsesDescription pins the within-milestone track
// reduction's prerequisite: the query requests the milestone description, and the
// raw markdown round-trips onto Milestone.Description with its markers intact. The
// struct↔query contract guard catches a renamed tag, but only a wire round-trip
// like this catches a tag that is wrong on both sides — and only this asserts the
// markdown is fetched raw (not plain-texted), which the parser depends on.
func TestListOpenMilestonesParsesDescription(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery = req.Query
		body := `{"data":{"repository":{"milestones":{
			"totalCount":1,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":7,"title":"M","url":"u7",
				 "description":"## Active tracks\n\n**Foundation** (critical-path): #10",
				 "open":{"totalCount":1},
				 "closed":{"totalCount":0}}
			]
		}}}}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := fetcherTo(srv.URL, "tok").ListOpenMilestones(context.Background(), "acme/widgets", 100)
	if err != nil {
		t.Fatalf("ListOpenMilestones: %v", err)
	}
	if !strings.Contains(gotQuery, "description") {
		t.Errorf("query does not request description; got:\n%s", gotQuery)
	}
	if len(res.Milestones) != 1 {
		t.Fatalf("got %d milestones, want 1", len(res.Milestones))
	}
	want := "## Active tracks\n\n**Foundation** (critical-path): #10"
	if res.Milestones[0].Description != want {
		t.Errorf("Description = %q, want raw markdown %q", res.Milestones[0].Description, want)
	}
}

// TestListOpenIssuesParsesMilestone pins that the open-issue query requests each
// issue's milestone and decodes it onto Issue.Milestone — present for a
// milestoned issue, nil for an unmilestoned one (GitHub returns null, leaving the
// pointer nil for the orientation reduction's unmilestoned signal).
func TestListOpenIssuesParsesMilestone(t *testing.T) {
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
			"totalCount":2,
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"number":1,"title":"a","url":"ua","createdAt":"2025-01-01T00:00:00Z",
				 "milestone":{"number":7,"title":"Round 5"},
				 "comments":{"nodes":[]}},
				{"number":2,"title":"b","url":"ub","createdAt":"2025-02-01T00:00:00Z",
				 "milestone":null,
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
	if !strings.Contains(gotQuery, "milestone{") {
		t.Errorf("query does not request milestone; got:\n%s", gotQuery)
	}
	if len(res.Issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(res.Issues))
	}
	m := res.Issues[0].Milestone
	if m == nil || m.Number != 7 || m.Title != "Round 5" {
		t.Errorf("issue 1 Milestone = %+v, want {7 Round 5}", m)
	}
	if res.Issues[1].Milestone != nil {
		t.Errorf("issue 2 Milestone = %+v, want nil (unmilestoned)", res.Issues[1].Milestone)
	}
}

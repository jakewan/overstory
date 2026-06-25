package github

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveSchemaAcceptance exercises every fetch shape against the real GitHub
// GraphQL API, so a query field GitHub's schema rejects (the one drift class the
// struct↔query contract test cannot see — it is nesting- and schema-blind) fails
// here instead of only in production. It is opt-in: set OVERSTORY_LIVE_REPO=
// owner/repo to run it — it needs real gh credentials and the network, so it
// stays skipped in ordinary CI.
func TestLiveSchemaAcceptance(t *testing.T) {
	repo := os.Getenv("OVERSTORY_LIVE_REPO")
	if repo == "" {
		t.Skip("set OVERSTORY_LIVE_REPO=owner/repo to run the live GitHub schema check (needs gh auth)")
	}
	f := NewGraphQLFetcher()
	// Bound the whole test explicitly. The HTTP client already caps each request at
	// 30s, but a deadline on the context keeps the total — across both fetches and
	// any pagination — from running away if the network stalls.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := f.ListOpenIssues(ctx, repo, 5); err != nil {
		t.Errorf("ListOpenIssues against %s: %v", repo, err)
	}
	if _, err := f.ListIssuesUpdatedSince(ctx, repo, time.Now().AddDate(0, 0, -90), 5); err != nil {
		t.Errorf("ListIssuesUpdatedSince against %s: %v", repo, err)
	}
	if _, err := f.ListPullRequestsUpdatedSince(ctx, repo, time.Now().AddDate(0, 0, -90), 5); err != nil {
		t.Errorf("ListPullRequestsUpdatedSince against %s: %v", repo, err)
	}
	if _, err := f.ListOpenMilestones(ctx, repo, 5); err != nil {
		t.Errorf("ListOpenMilestones against %s: %v", repo, err)
	}
	if _, err := f.ListOpenPullRequests(ctx, repo, 5); err != nil {
		t.Errorf("ListOpenPullRequests against %s: %v", repo, err)
	}
	// The REST issue-events fetch has no struct↔query contract test (it decodes a
	// bare JSON array, not a GraphQL query), so this live call is the only guard that
	// the real REST payload shape still maps into IssueEvent.
	if res, err := f.ListIssueEvents(ctx, repo, time.Now().AddDate(0, 0, -90), 50); err != nil {
		t.Errorf("ListIssueEvents against %s: %v", repo, err)
	} else if len(res.Events) > 0 && res.Events[0].EventID == 0 {
		t.Errorf("ListIssueEvents against %s decoded a zero event id — the REST payload shape may have drifted", repo)
	}
}

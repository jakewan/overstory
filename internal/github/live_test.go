package github

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveSchemaAcceptance exercises both fetch shapes against the real GitHub
// GraphQL API, so a query field GitHub's schema rejects (the one drift class the
// struct↔query contract test cannot see) fails here instead of only in
// production. It is opt-in: set OVERSTORY_LIVE_REPO=owner/repo to run it — it
// needs real gh credentials and the network, so it stays skipped in ordinary CI.
func TestLiveSchemaAcceptance(t *testing.T) {
	repo := os.Getenv("OVERSTORY_LIVE_REPO")
	if repo == "" {
		t.Skip("set OVERSTORY_LIVE_REPO=owner/repo to run the live GitHub schema check (needs gh auth)")
	}
	f := NewGraphQLFetcher()
	ctx := context.Background()

	if _, err := f.ListOpenIssues(ctx, repo, 5); err != nil {
		t.Errorf("ListOpenIssues against %s: %v", repo, err)
	}
	if _, err := f.ListIssuesUpdatedSince(ctx, repo, time.Now().AddDate(0, 0, -90), 5); err != nil {
		t.Errorf("ListIssuesUpdatedSince against %s: %v", repo, err)
	}
}

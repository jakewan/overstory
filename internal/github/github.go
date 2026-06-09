// Package github is overstory's in-process GitHub data layer. It fetches a
// repository's open issues via the GitHub GraphQL API, authenticated with the
// operator's existing gh credentials, and reduces each issue to the fields the
// staleness reduction needs — including a derived last-human-activity time, so
// label, assignment, and bot churn don't read as activity.
//
// Data is fetched in-process over net/http (no heavy client dependency); the
// only subprocess is `gh auth token` for credential bootstrap. The IssueFetcher
// interface is the seam that lets callers and tests substitute a fake.
package github

import (
	"context"
	"errors"
	"time"
)

// Issue is the subset of an open issue the staleness reduction consumes.
// LastActivityAt is the last human-comment time (else the issue's creation),
// derived in this layer so the reduction stays GitHub-agnostic.
type Issue struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	CreatedAt      time.Time `json:"createdAt"`
	LastActivityAt time.Time `json:"lastActivityAt"`
}

// IssueListResult carries the fetched issues plus the repository's exact open
// count. The window is truncated when len(Issues) < TotalOpen.
type IssueListResult struct {
	Issues    []Issue
	TotalOpen int
}

// IssueFetcher fetches a repository's open issues, newest-activity-last, up to
// fetchLimit. It exists so tests can substitute a fake without invoking gh or
// the network.
type IssueFetcher interface {
	ListOpenIssues(ctx context.Context, ownerRepo string, fetchLimit int) (IssueListResult, error)
}

// Sentinel errors classify the failure modes a caller acts on. They name the
// condition, not internal detail (in particular, never a manifest path).
var (
	// ErrGHNotFound means the gh CLI is not on PATH, so credentials can't be
	// obtained.
	ErrGHNotFound = errors.New("gh CLI not found on PATH")
	// ErrGHNotAuthed means gh is installed but not authenticated.
	ErrGHNotAuthed = errors.New("gh CLI is not authenticated; run 'gh auth login'")
	// ErrRepoNotFound means the repository does not exist or is not accessible
	// with the current credentials.
	ErrRepoNotFound = errors.New("repository not found or not accessible")
	// ErrRateLimited means the GitHub API rejected the request for rate limiting.
	ErrRateLimited = errors.New("GitHub API rate limit exceeded")
)

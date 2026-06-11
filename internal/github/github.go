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

// Issue is the subset of an open issue the backlog reductions consume.
// LastActivityAt is the last human-comment time (else the issue's creation),
// derived in this layer so the reductions stay GitHub-agnostic. Labels are the
// issue's label names as GitHub stores them (original casing); the deferred
// reduction matches them case-insensitively. BodyText is the issue body rendered
// to plain text (markdown formatting and HTML-comment template scaffolding
// stripped), so the quality reduction's "is there real prose here" length check
// isn't fooled by an empty issue-form stub.
//
// ReferencedBy is a filtered projection of this issue's cross-reference timeline,
// not raw GitHub data: the numbers of same-repository issues whose
// CrossReferencedEvent targets this one (pull-request and cross-repository
// sources are dropped, so a same-number issue in another repo can't collide),
// deduplicated and sorted. CrossRefsTruncated is set when the issue had more
// cross-reference events than the fetch cap, so the cross-reference reduction can
// flag that a graph edge may be missing rather than report incomplete data as
// complete.
type Issue struct {
	Number             int       `json:"number"`
	Title              string    `json:"title"`
	URL                string    `json:"url"`
	CreatedAt          time.Time `json:"createdAt"`
	LastActivityAt     time.Time `json:"lastActivityAt"`
	Labels             []string  `json:"labels"`
	BodyText           string    `json:"bodyText"`
	ReferencedBy       []int     `json:"referencedBy"`
	CrossRefsTruncated bool      `json:"crossRefsTruncated"`
}

// IssueListResult carries the fetched issues plus the repository's exact open
// count. The window is truncated when len(Issues) < TotalOpen. RateLimit is the
// most-recent budget snapshot observed across the paginated fetch, or nil when
// the response carried none, so a caller can pace itself.
type IssueListResult struct {
	Issues    []Issue
	TotalOpen int
	RateLimit *RateLimit
}

// RateLimit is the GraphQL points-budget snapshot from a successful fetch's
// rateLimit field: the points Remaining in the current window and the ResetAt
// instant the window refills. It matches the GitHub GraphQL rateLimit shape the
// issue scopes; limit/cost are intentionally not carried until a pacing consumer
// needs them.
type RateLimit struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
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

// RateLimitedError enriches ErrRateLimited with the recovery signal GitHub
// returned so a caller can decide when to retry rather than treat a transient
// throttle as a permanent failure. ResetAt is an absolute instant (from the
// X-RateLimit-Reset epoch header or a harvested GraphQL resetAt — no clock
// needed); RetryAfter is a relative duration (from a Retry-After seconds
// header). Either may be zero when GitHub sent no such signal, in which case the
// caller degrades to the plain rate-limit message.
//
// It carries no clock itself: resolving a relative RetryAfter to a wall-clock
// instant is the server's job, keeping this layer deterministic under test.
type RateLimitedError struct {
	ResetAt    time.Time
	RetryAfter time.Duration
}

// Error returns the plain sentinel text. The recovery detail is deliberately not
// rendered here: the server resolves it to an absolute wall-clock instant (this
// layer has no clock) and owns that presentation, so embedding a partial,
// clock-free rendering would only duplicate it.
func (e RateLimitedError) Error() string { return ErrRateLimited.Error() }

// Is reports RateLimitedError as a match for the ErrRateLimited sentinel, so
// existing errors.Is(err, ErrRateLimited) checks keep working while callers
// recover the reset detail via errors.As. The value receiver keeps the method
// in a value's method set, which is required because this error propagates by
// value (a pointer representation would break errors.As into a value target).
func (e RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

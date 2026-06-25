// Package github is overstory's in-process GitHub data layer. It fetches a
// repository's issues via the GitHub GraphQL API, authenticated with the
// operator's existing gh credentials, and reduces each issue to the fields a
// reduction needs. Two fetch shapes exist: the open-issue grooming window
// (ListOpenIssues — open issues by least-recent activity, with a derived
// last-human-activity time so label, assignment, and bot churn don't read as
// activity), and a lean open-and-closed activity window keyed on recent updates
// (ListIssuesUpdatedSince) that feeds the creation-vs-closure trajectory.
//
// Data is fetched in-process over net/http (no heavy client dependency); the
// only subprocess is `gh auth token` for credential bootstrap. The Fetcher
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
//
// Milestone is the issue's milestone association (number and title), or nil when
// the issue is unmilestoned — the orientation reduction reads it both to group
// issues under their milestone and to flag the unmilestoned ones.
type Issue struct {
	Number             int           `json:"number"`
	Title              string        `json:"title"`
	URL                string        `json:"url"`
	CreatedAt          time.Time     `json:"createdAt"`
	LastActivityAt     time.Time     `json:"lastActivityAt"`
	Labels             []string      `json:"labels"`
	BodyText           string        `json:"bodyText"`
	ReferencedBy       []int         `json:"referencedBy"`
	CrossRefsTruncated bool          `json:"crossRefsTruncated"`
	Milestone          *MilestoneRef `json:"milestone,omitempty"`
}

// MilestoneRef is an issue's milestone association: enough to group the issue and
// to name its milestone, without the open/closed progress counts a milestone fetch
// carries (those come from ListOpenMilestones, which reads them authoritatively
// from the milestone object rather than from the bounded issue window).
type MilestoneRef struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// Milestone is one open milestone with its authoritative progress: OpenIssues and
// ClosedIssues are the milestone object's own totals (not derived from the fetched
// issue window, so they stay exact even when the issue fetch truncates). URL lets a
// caller link the milestone, mirroring Issue.URL. Description is the milestone's
// *raw markdown* body (not plain text, unlike Issue.BodyText): the within-milestone
// track reduction parses its structure, so the markdown markers must survive.
type Milestone struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	OpenIssues   int    `json:"openIssues"`
	ClosedIssues int    `json:"closedIssues"`
	Description  string `json:"description"`
}

// MilestoneListResult carries the fetched open milestones plus the repository's
// exact open-milestone count; the window is truncated when len(Milestones) <
// TotalOpen. RateLimit is the most-recent budget snapshot observed across the
// paginated fetch, or nil when the response carried none.
type MilestoneListResult struct {
	Milestones []Milestone
	TotalOpen  int
	RateLimit  *RateLimit
}

// PullRequest is the subset of an open pull request the orientation reduction
// consumes: identity, draft/ready state, its head branch, and the rolled-up CI
// status. LastActivityAt is the PR's updatedAt (the stale-PR signal measures from
// it). CIStatus is the head commit's status-check rollup state (e.g. SUCCESS,
// FAILURE, PENDING, ERROR, EXPECTED); it is empty when the rollup is null — the PR
// has no checks reported — which a caller reads as "no rollup", distinct from the
// reported pending/expected states.
type PullRequest struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	IsDraft        bool      `json:"isDraft"`
	CreatedAt      time.Time `json:"createdAt"`
	LastActivityAt time.Time `json:"lastActivityAt"`
	HeadRefName    string    `json:"headRefName"`
	CIStatus       string    `json:"ciStatus"`
}

// PullRequestListResult carries the fetched open pull requests plus the
// repository's exact open-PR count; the window is truncated when
// len(PullRequests) < TotalOpen. RateLimit is the most-recent budget snapshot
// observed across the paginated fetch, or nil when the response carried none.
type PullRequestListResult struct {
	PullRequests []PullRequest
	TotalOpen    int
	RateLimit    *RateLimit
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

// IssueActivity is the minimal projection of an issue the trajectory reduction
// needs: its number and its create/close instants. ClosedAt is the zero time
// when the issue is currently open — including a reopened issue, whose closedAt
// GitHub clears on reopen, so a close that was later reopened reads as open (the
// "net backlog change as of now" semantics). CreatedAt and ClosedAt are UTC, as
// the GraphQL API returns them.
type IssueActivity struct {
	Number    int       `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	ClosedAt  time.Time `json:"closedAt"`
}

// IssueActivityResult carries the issues fetched for the trajectory reduction —
// those updated at or after the requested instant, both open and closed.
// Truncated is true when the fetch cap was reached before the activity window was
// fully covered, so the trajectory counts derived from it are lower bounds, not
// exact; it is the activity fetch's only truncation seam (the window has no
// TotalOpen analog). RateLimit is the most-recent budget snapshot, or nil when
// the response carried none.
type IssueActivityResult struct {
	Activities []IssueActivity
	Truncated  bool
	RateLimit  *RateLimit
}

// PullRequestActivity is the minimal projection of a pull request the PR-
// trajectory reduction needs: its number and its open/close instants. ClosedAt is
// the zero time when the PR is currently open, and is populated for both a merged
// PR and one closed without merge (merging closes the PR), so it captures all
// outflow — the PR analog of IssueActivity.ClosedAt. CreatedAt and ClosedAt are
// UTC, as the GraphQL API returns them.
type PullRequestActivity struct {
	Number    int       `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	ClosedAt  time.Time `json:"closedAt"`
}

// PullRequestActivityResult carries the pull requests fetched for the PR-
// trajectory reduction — those updated at or after the requested instant, open
// and closed/merged alike. Truncated is true when the fetch cap was reached before
// the activity window was fully covered, so the trajectory counts derived from it
// are lower bounds, not exact; it is the fetch's only truncation seam. RateLimit
// is the most-recent budget snapshot, or nil when the response carried none.
type PullRequestActivityResult struct {
	Activities []PullRequestActivity
	Truncated  bool
	RateLimit  *RateLimit
}

// AuthoredActivityResult carries the decomposed authored/engagement counts for
// one user in one repository over a bounded window: the six categories the
// attention-audit consumer reads, kept as separate numbers (never summed) so the
// consumer owns any weighting. There is no truncation seam — these are counts,
// not a bounded list. RateLimit is the budget snapshot from the fetch, or nil
// when the responses carried none.
//
// The categories' precision differs and is documented per-category by the
// reduction's fidelity labels: CommitsAuthored is the default-branch commit count
// attributed to the author's linked identity; IssuesOpened/PullRequestsOpened
// count items the author created in the window; ReviewsSubmitted counts others'
// PRs the author reviewed (peer review, excluding the author's own PRs);
// PullRequestsEngaged/IssuesEngaged count items the author commented on but did
// not author.
type AuthoredActivityResult struct {
	CommitsAuthored     int
	IssuesOpened        int
	PullRequestsOpened  int
	ReviewsSubmitted    int
	PullRequestsEngaged int
	IssuesEngaged       int
	RateLimit           *RateLimit
}

// IssueEvent is one actor-attributed state mutation on an issue or pull request,
// projected from GitHub's REST issue-events stream. Type is the raw event string
// (labeled, milestoned, closed, …); the maintenance reduction filters to the
// mutation subset it cares about. Actor is the login that performed it; the
// reduction filters by it (the stream is not server-side filtered by actor).
// IssueIsPR distinguishes a pull request from an issue (the stream mixes both),
// so a caller can split the two. ViaAutomation is set when GitHub attributes the
// event to a GitHub App (the payload's performed_via_github_app is present), so a
// caller can exclude workflow/app-driven churn — with the blind spot that an
// automation running *as* the measured login is still attributed to that login.
// EventID is GitHub's unique, monotonically increasing event id, used to
// deduplicate across pages when a concurrent write shifts the offset window.
// Label/Milestone/Assignee/RenameFrom/RenameTo carry the per-type payload, empty
// for event types that don't populate them.
//
// It is a flattened domain type, not the REST wire shape: the nested response
// (actor.login, issue.number, issue.pull_request, label.name, …) decodes into a
// private wire struct that maps into this.
type IssueEvent struct {
	EventID       int64
	Type          string
	Actor         string
	CreatedAt     time.Time
	IssueNumber   int
	IssueTitle    string
	IssueIsPR     bool
	ViaAutomation bool
	Label         string
	Milestone     string
	Assignee      string
	RenameFrom    string
	RenameTo      string
}

// IssueEventsResult carries the issue/PR events fetched for the maintenance
// reduction — the repository's most-recent state mutations, newest-first, scanned
// back to the requested floor. Truncated is true when the scan stopped before it
// could prove window coverage (the fetch cap was reached without crossing the
// floor or exhausting the stream), so the maintenance facts derived from it are a
// lower bound, not exact. RateLimit is the REST core-pool budget from the
// response headers, which the REST endpoint always returns (unlike the GraphQL
// budget, this is essentially never nil).
type IssueEventsResult struct {
	Events    []IssueEvent
	Truncated bool
	RateLimit *RateLimit
}

// Fetcher fetches a repository's issues, milestones, and pull requests for the
// reductions. It exists so tests can substitute a fake without invoking gh or the
// network. ListOpenIssues returns the open-issue grooming window
// (newest-activity-last, up to fetchLimit); ListIssuesUpdatedSince returns the
// lean open-and-closed activity window updated at or after `since` (up to
// fetchLimit), feeding the creation-vs-closure trajectory;
// ListPullRequestsUpdatedSince returns the lean open-and-closed/merged
// pull-request activity window updated at or after `since` (up to fetchLimit),
// feeding the change-request closure-ratio (PR-trajectory) reduction;
// ListOpenMilestones
// returns the open milestones with their authoritative open/closed counts, for
// the orientation reduction's milestone-progress view; ListOpenPullRequests
// returns the open pull requests with draft state, head branch, and CI rollup,
// for the orientation reduction's in-flight-work view; AuthoredActivity returns
// the decomposed authored/engagement counts for one author over the [since,until]
// window (author- and window-driven, manifest-independent); ListIssueEvents
// returns the repository's issue/PR state-mutation events back to `since`
// (newest-first, up to fetchLimit), feeding the maintenance reduction — the only
// REST-sourced fetch, since the events stream has no GraphQL equivalent the other
// shapes use.
type Fetcher interface {
	ListOpenIssues(ctx context.Context, ownerRepo string, fetchLimit int) (IssueListResult, error)
	ListIssuesUpdatedSince(ctx context.Context, ownerRepo string, since time.Time, fetchLimit int) (IssueActivityResult, error)
	ListPullRequestsUpdatedSince(ctx context.Context, ownerRepo string, since time.Time, fetchLimit int) (PullRequestActivityResult, error)
	ListOpenMilestones(ctx context.Context, ownerRepo string, fetchLimit int) (MilestoneListResult, error)
	ListOpenPullRequests(ctx context.Context, ownerRepo string, fetchLimit int) (PullRequestListResult, error)
	AuthoredActivity(ctx context.Context, ownerRepo, author string, since, until time.Time) (AuthoredActivityResult, error)
	ListIssueEvents(ctx context.Context, ownerRepo string, since time.Time, fetchLimit int) (IssueEventsResult, error)
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
	// ErrAuthorNotFound means the requested author login does not resolve to a
	// GitHub user, so authored-activity counts would be six meaningless zeros
	// indistinguishable from a real-but-inactive user — surfaced as an error
	// rather than coerced to zero.
	ErrAuthorNotFound = errors.New("author login not found")
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

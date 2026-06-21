package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEndpoint = "https://api.github.com/graphql"
	pageSize        = 100
	httpTimeout     = 30 * time.Second
	// userAgent identifies overstory to GitHub. The API expects a descriptive
	// User-Agent; relying on the net/http default risks being throttled or
	// blocked.
	userAgent = "overstory"
)

// issuesQuery fetches a page of open issues newest-activity-last. owner/name are
// GraphQL variables, not interpolated, so caller-supplied values can never
// become query structure. UPDATED_AT ASC is the closest available proxy for
// "least recently active first"; comments(last:25) bounds the window scanned to
// derive last-human activity; labels(first:25) bounds the labels read per issue
// (an issue with >25 labels could miss a label in the tail, so the deferred,
// area-balance, and quality signals can misread such an issue — acceptable for a
// grooming signal). bodyText is the rendered-plaintext body (markdown and
// HTML-comment scaffolding stripped) for the quality reduction's length check;
// the raw markdown body is deliberately not fetched until a later increment needs
// it, so unread payload doesn't bloat this shared fetch. milestone{number title}
// associates each issue with its milestone (null when unmilestoned) for the
// orientation reduction's milestone grouping and unmilestoned-issue signal.
//
// timelineItems(itemTypes:[CROSS_REFERENCED_EVENT], last:25) feeds the
// cross-reference reduction. The itemTypes filter applies to the connection, so
// totalCount counts the cross-reference events alone and last:25 bounds them per
// issue; the cap is modest to bound query node cost, and totalCount lets the
// decode flag a per-issue truncation rather than drop edges silently. The event
// sits on the *referenced* issue's timeline, so source is the issue/PR that
// referenced this one — an incoming edge.
const issuesQuery = `query($owner:String!,$name:String!,$first:Int!,$after:String){
  rateLimit{ remaining resetAt }
  repository(owner:$owner,name:$name){
    issues(states:OPEN, first:$first, after:$after, orderBy:{field:UPDATED_AT, direction:ASC}){
      totalCount
      pageInfo{ hasNextPage endCursor }
      nodes{
        number title url createdAt bodyText
        milestone{ number title }
        labels(first:25){ nodes{ name } }
        comments(last:25){ nodes{ createdAt author{ __typename login } } }
        timelineItems(itemTypes:[CROSS_REFERENCED_EVENT], last:25){
          totalCount
          nodes{ __typename ... on CrossReferencedEvent {
            isCrossRepository
            source{ __typename ... on Issue { number } }
          } }
        }
      }
    }
  }
}`

// activityQuery fetches a page of issues — open AND closed — ordered by most
// recent update first, for the creation-vs-closure trajectory. The DESC-by-
// updatedAt order is what lets ListIssuesUpdatedSince stop once it reaches an
// issue updated before the window floor: every issue created or closed in the
// window has updatedAt >= that event >= the floor, so it sorts ahead of the stop
// point. The node selection is deliberately lean — number, createdAt, closedAt,
// updatedAt only — so spanning closed issues stays cheap; labels, comments,
// body, and the timeline are not needed here. closedAt is null for an open (or
// reopened) issue.
const activityQuery = `query($owner:String!,$name:String!,$first:Int!,$after:String){
  rateLimit{ remaining resetAt }
  repository(owner:$owner,name:$name){
    issues(states:[OPEN,CLOSED], first:$first, after:$after, orderBy:{field:UPDATED_AT, direction:DESC}){
      pageInfo{ hasNextPage endCursor }
      nodes{ number createdAt closedAt updatedAt }
    }
  }
}`

// milestonesQuery fetches a page of open milestones for the orientation
// reduction's milestone-progress view, ordered by number for a stable page
// sequence. Each milestone's open/closed issue counts come from the milestone
// object's own issue connections (open:/closed: aliases over issues(states:…){
// totalCount }) rather than from the bounded issue window, so the counts stay
// authoritative even when the issue fetch truncates. The aliases decode onto
// distinct struct fields; the connection's own totalCount is the open-milestone
// total for the window-truncation seam. description is the raw markdown body the
// within-milestone track reduction parses — fetched raw, not as plain text
// (GitHub's Milestone type exposes no plain-text variant), so its structure
// survives.
const milestonesQuery = `query($owner:String!,$name:String!,$first:Int!,$after:String){
  rateLimit{ remaining resetAt }
  repository(owner:$owner,name:$name){
    milestones(states:OPEN, first:$first, after:$after, orderBy:{field:NUMBER, direction:ASC}){
      totalCount
      pageInfo{ hasNextPage endCursor }
      nodes{
        number title url description
        open: issues(states:OPEN){ totalCount }
        closed: issues(states:CLOSED){ totalCount }
      }
    }
  }
}`

// pullRequestsQuery fetches a page of open pull requests for the orientation
// reduction's in-flight-work view, ordered like the issue grooming window
// (UPDATED_AT ASC). headRefName is the PR's source branch; isDraft separates
// draft from ready. commits(last:1) reads the head commit's statusCheckRollup —
// the single aggregate CI verdict across all checks — bounding the fetch to one
// commit's rollup rather than walking every check. statusCheckRollup is null when
// the PR has no checks reported, which decodes to an empty CIStatus.
const pullRequestsQuery = `query($owner:String!,$name:String!,$first:Int!,$after:String){
  rateLimit{ remaining resetAt }
  repository(owner:$owner,name:$name){
    pullRequests(states:OPEN, first:$first, after:$after, orderBy:{field:UPDATED_AT, direction:ASC}){
      totalCount
      pageInfo{ hasNextPage endCursor }
      nodes{
        number title url isDraft createdAt updatedAt headRefName
        commits(last:1){ nodes{ commit{ statusCheckRollup{ state } } } }
      }
    }
  }
}`

// authoredSearchQuery resolves the author's user id and the five search counts in
// one root-level request (not repository-rooted — search and user are root
// fields, so this decodes via doRaw, not do). Each search reads only issueCount
// with first:0, so no nodes are paged — the count comes back directly. The five
// query strings are passed as variables ($q0..$q4) rather than interpolated, so
// caller-supplied values can never become query structure; the qualifiers inside
// them (is:issue/is:pr, author:/commenter:/reviewed-by:, the window) are asserted
// by a dedicated string test, since the query-contract guard cannot see inside a
// search string argument. type:ISSUE searches issues AND pull requests; the
// is:issue/is:pr qualifier in each string is what splits them.
const authoredSearchQuery = `query($author:String!,$q0:String!,$q1:String!,$q2:String!,$q3:String!,$q4:String!){
  rateLimit{ remaining resetAt }
  user(login:$author){ id }
  s0: search(query:$q0, type:ISSUE, first:0){ issueCount }
  s1: search(query:$q1, type:ISSUE, first:0){ issueCount }
  s2: search(query:$q2, type:ISSUE, first:0){ issueCount }
  s3: search(query:$q3, type:ISSUE, first:0){ issueCount }
  s4: search(query:$q4, type:ISSUE, first:0){ issueCount }
}`

// commitHistoryQuery counts the author's commits on the repository's default
// branch within the window, filtered by the resolved user id. history.totalCount
// is the exact count (no node paging). A null defaultBranchRef/target (empty
// default branch) decodes to a zero count.
const commitHistoryQuery = `query($owner:String!,$name:String!,$id:ID!,$since:GitTimestamp!,$until:GitTimestamp!){
  rateLimit{ remaining resetAt }
  repository(owner:$owner,name:$name){
    defaultBranchRef{
      target{
        ... on Commit {
          history(author:{id:$id}, since:$since, until:$until){ totalCount }
        }
      }
    }
  }
}`

// GraphQLFetcher fetches open issues via the GitHub GraphQL API in-process.
// endpoint, tokens, and client are fields so tests can drive it against an
// httptest.Server with a static token.
type GraphQLFetcher struct {
	endpoint string
	tokens   TokenSource
	client   *http.Client
}

// NewGraphQLFetcher builds the production fetcher: GitHub.com's GraphQL
// endpoint, credentials from the operator's gh CLI, and a timeout-bounded HTTP
// client. The token is acquired lazily on first fetch, so construction is
// side-effect-free.
func NewGraphQLFetcher() *GraphQLFetcher {
	return &GraphQLFetcher{
		endpoint: defaultEndpoint,
		tokens:   &GHTokenSource{},
		client:   &http.Client{Timeout: httpTimeout},
	}
}

// ListOpenIssues fetches up to fetchLimit open issues, paginating until the
// limit is reached or the connection is exhausted, and reports the repository's
// exact open count via TotalOpen.
func (f *GraphQLFetcher) ListOpenIssues(ctx context.Context, ownerRepo string, fetchLimit int) (IssueListResult, error) {
	owner, name, err := splitOwnerRepo(ownerRepo)
	if err != nil {
		return IssueListResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return IssueListResult{}, err
	}

	var (
		issues    []Issue
		totalOpen int
		cursor    *string
		// budget tracks the most recent page's rateLimit (nil included), so the
		// freshest observation wins and a final page that omits it clears a stale
		// earlier value rather than reporting an optimistic one.
		budget *RateLimit
	)
	maxPages := fetchLimit/pageSize + 2 // loop guard against a misbehaving connection
	for range maxPages {
		first := pageSize
		if remaining := fetchLimit - len(issues); remaining < first {
			first = remaining
		}
		if first <= 0 {
			break
		}
		conn, pageBudget, qerr := f.query(ctx, token, owner, name, first, cursor)
		if qerr != nil {
			return IssueListResult{}, qerr
		}
		budget = pageBudget
		totalOpen = conn.TotalCount
		for _, n := range conn.Nodes {
			issues = append(issues, n.toIssue())
		}
		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			break
		}
		if cursor != nil && *cursor == conn.PageInfo.EndCursor {
			break // cursor failed to advance; stop rather than loop forever
		}
		next := conn.PageInfo.EndCursor
		cursor = &next
		if len(issues) >= fetchLimit {
			break
		}
	}
	return IssueListResult{Issues: issues, TotalOpen: totalOpen, RateLimit: budget}, nil
}

// query fetches one page of the open-issue grooming window, decoding the shared
// spine's raw repository payload into the open-issue connection.
func (f *GraphQLFetcher) query(ctx context.Context, token, owner, name string, first int, after *string) (issuesConnection, *RateLimit, error) {
	repo, budget, err := f.do(ctx, token, issuesQuery, queryVars(owner, name, first, after), owner, name)
	if err != nil {
		return issuesConnection{}, nil, err
	}
	var data struct {
		Issues issuesConnection `json:"issues"`
	}
	if derr := json.Unmarshal(repo, &data); derr != nil {
		return issuesConnection{}, nil, fmt.Errorf("decoding GitHub issues for %s/%s: %w", owner, name, derr)
	}
	return data.Issues, budget, nil
}

// queryActivity fetches one page of the open-and-closed activity window for the
// trajectory reduction, decoding the shared spine's payload into the lean
// activity connection.
func (f *GraphQLFetcher) queryActivity(ctx context.Context, token, owner, name string, first int, after *string) (activityConnection, *RateLimit, error) {
	repo, budget, err := f.do(ctx, token, activityQuery, queryVars(owner, name, first, after), owner, name)
	if err != nil {
		return activityConnection{}, nil, err
	}
	var data struct {
		Issues activityConnection `json:"issues"`
	}
	if derr := json.Unmarshal(repo, &data); derr != nil {
		return activityConnection{}, nil, fmt.Errorf("decoding GitHub activity for %s/%s: %w", owner, name, derr)
	}
	return data.Issues, budget, nil
}

// queryVars builds the GraphQL variables shared by both fetch shapes; after is
// omitted on the first page so the query's default null applies.
func queryVars(owner, name string, first int, after *string) map[string]any {
	vars := map[string]any{"owner": owner, "name": name, "first": first}
	if after != nil {
		vars["after"] = *after
	}
	return vars
}

// doRaw executes one GraphQL request and returns the raw data block plus the
// rateLimit budget, after status and GraphQL-error classification. Unlike do it
// makes no assumption about a repository-rooted query, so a root-level fetch
// (user, search) can decode its own fields from data — do builds on it for the
// repository-rooted shapes. The rateLimit node is peeked from data and passed
// into classifyGraphQLErrors so a RATE_LIMITED error can fall back to its resetAt
// when the response carried no rate headers — the throttle recovery signal the
// server surfaces.
func (f *GraphQLFetcher) doRaw(ctx context.Context, token, query string, vars map[string]any, owner, name string) (data json.RawMessage, budget *RateLimit, err error) {
	payload, merr := json.Marshal(map[string]any{"query": query, "variables": vars})
	if merr != nil {
		return nil, nil, fmt.Errorf("encoding GraphQL query: %w", merr)
	}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(payload))
	if rerr != nil {
		return nil, nil, fmt.Errorf("building request: %w", rerr)
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, doErr := f.client.Do(req)
	if doErr != nil {
		return nil, nil, fmt.Errorf("querying GitHub for %s/%s: %w", owner, name, doErr)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing GitHub response body: %w", cerr)
		}
	}()

	if statusErr := classifyStatus(resp.StatusCode, resp.Header, owner, name); statusErr != nil {
		return nil, nil, statusErr
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if decErr := json.NewDecoder(resp.Body).Decode(&env); decErr != nil {
		return nil, nil, fmt.Errorf("decoding GitHub response for %s/%s: %w", owner, name, decErr)
	}
	// rateLimit sits at the data root in every query; peek it out so a
	// RATE_LIMITED error can fall back to its resetAt and so the success budget
	// surfaces. A null/absent data block leaves it nil.
	var root struct {
		RateLimit *rateLimitNode `json:"rateLimit"`
	}
	if len(env.Data) > 0 {
		if uerr := json.Unmarshal(env.Data, &root); uerr != nil {
			return nil, nil, fmt.Errorf("decoding GitHub response for %s/%s: %w", owner, name, uerr)
		}
	}
	// GraphQL can return a 200 carrying an errors array (e.g. NOT_FOUND with a
	// null repository), so this is checked regardless of HTTP status.
	if gqlErr := classifyGraphQLErrors(env.Errors, resp.Header, root.RateLimit, owner, name); gqlErr != nil {
		return nil, nil, gqlErr
	}
	return env.Data, toRateLimit(root.RateLimit), nil
}

// do executes one repository-rooted GraphQL request and returns the raw
// data.repository payload plus the rateLimit budget, leaving connection/node
// decoding to each caller. It is the single home for the null-repository check
// the repository-rooted fetch shapes share, built on doRaw's request spine.
func (f *GraphQLFetcher) do(ctx context.Context, token, query string, vars map[string]any, owner, name string) (json.RawMessage, *RateLimit, error) {
	data, budget, err := f.doRaw(ctx, token, query, vars, owner, name)
	if err != nil {
		return nil, nil, err
	}
	var d struct {
		Repository *json.RawMessage `json:"repository"`
	}
	if len(data) > 0 {
		if uerr := json.Unmarshal(data, &d); uerr != nil {
			return nil, nil, fmt.Errorf("decoding GitHub response for %s/%s: %w", owner, name, uerr)
		}
	}
	if d.Repository == nil {
		return nil, nil, fmt.Errorf("%s/%s: %w", owner, name, ErrRepoNotFound)
	}
	return *d.Repository, budget, nil
}

// ListIssuesUpdatedSince fetches issues (open and closed) updated at or after
// `since`, newest-update-first, up to fetchLimit, for the trajectory reduction.
// It pages until it observes an issue updated before `since` — the window floor,
// past which no in-window create or close can sort — or the connection is
// exhausted. Truncated is reported floor/exhaustion-driven, not stop-reason-
// driven: it is false only when the scan proved coverage (crossed the floor, or
// drained the connection); every early exit (the fetch cap, the page guard, a
// stalled cursor) leaves it true, so the trajectory counts are never reported as
// complete when they are a lower bound.
func (f *GraphQLFetcher) ListIssuesUpdatedSince(ctx context.Context, ownerRepo string, since time.Time, fetchLimit int) (IssueActivityResult, error) {
	owner, name, err := splitOwnerRepo(ownerRepo)
	if err != nil {
		return IssueActivityResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return IssueActivityResult{}, err
	}

	var (
		activities   []IssueActivity
		cursor       *string
		budget       *RateLimit
		crossedFloor bool // saw an issue updated before the floor: everything in-window precedes it
		exhausted    bool // drained the connection: nothing more to fetch
	)
	maxPages := fetchLimit/pageSize + 2 // loop guard against a misbehaving connection
	for range maxPages {
		first := pageSize
		if remaining := fetchLimit - len(activities); remaining < first {
			first = remaining
		}
		if first <= 0 {
			break
		}
		conn, pageBudget, qerr := f.queryActivity(ctx, token, owner, name, first, cursor)
		if qerr != nil {
			return IssueActivityResult{}, qerr
		}
		budget = pageBudget // last page wins, including nil, so a stale budget is cleared
		for _, nd := range conn.Nodes {
			if nd.UpdatedAt.Before(since) {
				crossedFloor = true
				break
			}
			activities = append(activities, nd.toActivity())
		}
		if crossedFloor {
			break
		}
		if !conn.PageInfo.HasNextPage {
			exhausted = true // connection drained: coverage is complete
			break
		}
		if conn.PageInfo.EndCursor == "" {
			break // more pages exist but no cursor to fetch them: coverage unproven, leave truncated
		}
		if cursor != nil && *cursor == conn.PageInfo.EndCursor {
			break // cursor failed to advance; stop rather than loop forever (coverage unproven)
		}
		next := conn.PageInfo.EndCursor
		cursor = &next
		if len(activities) >= fetchLimit {
			break
		}
	}
	return IssueActivityResult{
		Activities: activities,
		// Truncated unless coverage was proven: either the floor was crossed or the
		// connection was drained. Every other exit leaves the window unproven.
		Truncated: !crossedFloor && !exhausted,
		RateLimit: budget,
	}, nil
}

// ListOpenMilestones fetches up to fetchLimit open milestones, paginating until
// the limit is reached or the connection is exhausted, and reports the
// repository's exact open-milestone count via TotalOpen. Each milestone carries
// its authoritative open/closed issue counts, read from the milestone object
// rather than derived from any issue window.
func (f *GraphQLFetcher) ListOpenMilestones(ctx context.Context, ownerRepo string, fetchLimit int) (MilestoneListResult, error) {
	owner, name, err := splitOwnerRepo(ownerRepo)
	if err != nil {
		return MilestoneListResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return MilestoneListResult{}, err
	}

	var (
		milestones []Milestone
		totalOpen  int
		cursor     *string
		budget     *RateLimit
	)
	maxPages := fetchLimit/pageSize + 2 // loop guard against a misbehaving connection
	for range maxPages {
		first := pageSize
		if remaining := fetchLimit - len(milestones); remaining < first {
			first = remaining
		}
		if first <= 0 {
			break
		}
		conn, pageBudget, qerr := f.queryMilestones(ctx, token, owner, name, first, cursor)
		if qerr != nil {
			return MilestoneListResult{}, qerr
		}
		budget = pageBudget
		totalOpen = conn.TotalCount
		for _, n := range conn.Nodes {
			milestones = append(milestones, n.toMilestone())
		}
		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			break
		}
		if cursor != nil && *cursor == conn.PageInfo.EndCursor {
			break // cursor failed to advance; stop rather than loop forever
		}
		next := conn.PageInfo.EndCursor
		cursor = &next
		if len(milestones) >= fetchLimit {
			break
		}
	}
	return MilestoneListResult{Milestones: milestones, TotalOpen: totalOpen, RateLimit: budget}, nil
}

// queryMilestones fetches one page of open milestones, decoding the shared
// spine's raw repository payload into the milestone connection.
func (f *GraphQLFetcher) queryMilestones(ctx context.Context, token, owner, name string, first int, after *string) (milestonesConnection, *RateLimit, error) {
	repo, budget, err := f.do(ctx, token, milestonesQuery, queryVars(owner, name, first, after), owner, name)
	if err != nil {
		return milestonesConnection{}, nil, err
	}
	var data struct {
		Milestones milestonesConnection `json:"milestones"`
	}
	if derr := json.Unmarshal(repo, &data); derr != nil {
		return milestonesConnection{}, nil, fmt.Errorf("decoding GitHub milestones for %s/%s: %w", owner, name, derr)
	}
	return data.Milestones, budget, nil
}

// ListOpenPullRequests fetches up to fetchLimit open pull requests, paginating
// until the limit is reached or the connection is exhausted, and reports the
// repository's exact open-PR count via TotalOpen.
func (f *GraphQLFetcher) ListOpenPullRequests(ctx context.Context, ownerRepo string, fetchLimit int) (PullRequestListResult, error) {
	owner, name, err := splitOwnerRepo(ownerRepo)
	if err != nil {
		return PullRequestListResult{}, err
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return PullRequestListResult{}, err
	}

	var (
		prs       []PullRequest
		totalOpen int
		cursor    *string
		budget    *RateLimit
	)
	maxPages := fetchLimit/pageSize + 2 // loop guard against a misbehaving connection
	for range maxPages {
		first := pageSize
		if remaining := fetchLimit - len(prs); remaining < first {
			first = remaining
		}
		if first <= 0 {
			break
		}
		conn, pageBudget, qerr := f.queryPullRequests(ctx, token, owner, name, first, cursor)
		if qerr != nil {
			return PullRequestListResult{}, qerr
		}
		budget = pageBudget
		totalOpen = conn.TotalCount
		for _, n := range conn.Nodes {
			prs = append(prs, n.toPullRequest())
		}
		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			break
		}
		if cursor != nil && *cursor == conn.PageInfo.EndCursor {
			break // cursor failed to advance; stop rather than loop forever
		}
		next := conn.PageInfo.EndCursor
		cursor = &next
		if len(prs) >= fetchLimit {
			break
		}
	}
	return PullRequestListResult{PullRequests: prs, TotalOpen: totalOpen, RateLimit: budget}, nil
}

// queryPullRequests fetches one page of open pull requests, decoding the shared
// spine's raw repository payload into the pull-request connection.
func (f *GraphQLFetcher) queryPullRequests(ctx context.Context, token, owner, name string, first int, after *string) (pullRequestsConnection, *RateLimit, error) {
	repo, budget, err := f.do(ctx, token, pullRequestsQuery, queryVars(owner, name, first, after), owner, name)
	if err != nil {
		return pullRequestsConnection{}, nil, err
	}
	var data struct {
		PullRequests pullRequestsConnection `json:"pullRequests"`
	}
	if derr := json.Unmarshal(repo, &data); derr != nil {
		return pullRequestsConnection{}, nil, fmt.Errorf("decoding GitHub pull requests for %s/%s: %w", owner, name, derr)
	}
	return data.PullRequests, budget, nil
}

// AuthoredActivity counts what `author` authored and engaged with in ownerRepo
// over [since, until]. It runs two requests (≈7 server-side operations: a user
// resolve plus five searches in the first, a commit-history count in the second;
// each search has its own index-consistency and secondary-rate-limit exposure):
// the root-level search/user request resolves the author and reads the five
// search counts, then — only when the author resolved — the repository-rooted
// request counts default-branch commits. An unresolved login is ErrAuthorNotFound
// (naming the login), never coerced to zero counts.
func (f *GraphQLFetcher) AuthoredActivity(ctx context.Context, ownerRepo, author string, since, until time.Time) (AuthoredActivityResult, error) {
	owner, name, err := splitOwnerRepo(ownerRepo)
	if err != nil {
		return AuthoredActivityResult{}, err
	}
	author = strings.TrimSpace(author)
	if author == "" {
		return AuthoredActivityResult{}, fmt.Errorf("author login is required")
	}
	token, err := f.tokens.Token(ctx)
	if err != nil {
		return AuthoredActivityResult{}, err
	}

	// One window, one instant: the same RFC3339 UTC bounds drive both the search
	// date qualifiers and the history GitTimestamps, so the two requests can't
	// straddle a day boundary differently.
	sinceStr := since.UTC().Format(time.RFC3339)
	untilStr := until.UTC().Format(time.RFC3339)
	qs := authoredSearchQueries(owner+"/"+name, author, sinceStr, untilStr)
	vars1 := map[string]any{
		"author": author,
		"q0":     qs[0], "q1": qs[1], "q2": qs[2], "q3": qs[3], "q4": qs[4],
	}
	data, budget1, err := f.doRaw(ctx, token, authoredSearchQuery, vars1, owner, name)
	if err != nil {
		return AuthoredActivityResult{}, err
	}
	var sd authoredSearchData
	if derr := json.Unmarshal(data, &sd); derr != nil {
		return AuthoredActivityResult{}, fmt.Errorf("decoding authored activity for %s/%s: %w", owner, name, derr)
	}
	if sd.User == nil {
		// A null user is not a GraphQL error, so classify it here: surfacing it as
		// six zeros would be indistinguishable from a real-but-inactive user.
		return AuthoredActivityResult{}, fmt.Errorf("%q in %s/%s: %w", author, owner, name, ErrAuthorNotFound)
	}

	vars2 := map[string]any{"owner": owner, "name": name, "id": sd.User.ID, "since": sinceStr, "until": untilStr}
	repo, budget2, err := f.do(ctx, token, commitHistoryQuery, vars2, owner, name)
	if err != nil {
		return AuthoredActivityResult{}, err
	}
	var cd commitHistoryData
	if derr := json.Unmarshal(repo, &cd); derr != nil {
		return AuthoredActivityResult{}, fmt.Errorf("decoding commit history for %s/%s: %w", owner, name, derr)
	}
	commits := 0
	if cd.DefaultBranchRef != nil && cd.DefaultBranchRef.Target != nil {
		commits = cd.DefaultBranchRef.Target.History.TotalCount
	}

	// The second request is the later observation, so its budget wins; fall back
	// to the first when the second carried none.
	budget := budget2
	if budget == nil {
		budget = budget1
	}
	return AuthoredActivityResult{
		CommitsAuthored:     commits,
		IssuesOpened:        sd.S0.IssueCount,
		PullRequestsOpened:  sd.S1.IssueCount,
		ReviewsSubmitted:    sd.S2.IssueCount,
		PullRequestsEngaged: sd.S3.IssueCount,
		IssuesEngaged:       sd.S4.IssueCount,
		RateLimit:           budget,
	}, nil
}

// authoredSearchQueries assembles the five GitHub search query strings in the
// order the authoredSearchQuery aliases consume them (s0..s4). Issues/PRs opened
// filter on created date (the authored event); reviews and the two engagement
// categories filter on the item's updated date — an approximation, since search
// cannot filter by comment/review date. The -author exclusion isolates attention
// to others' work — peer review and engagement — from the author's own items: it
// keeps reviewsSubmitted to peer review (GitHub wraps every inline PR comment in a
// review object, so replies on one's own PR would otherwise inflate it) and the
// two engagement counts to others' threads. Window is one shared RFC3339 UTC pair.
// The result order is load-bearing and pinned by test.
func authoredSearchQueries(repo, author, since, until string) [5]string {
	created := "created:" + since + ".." + until
	updated := "updated:" + since + ".." + until
	base := "repo:" + repo
	return [5]string{
		base + " is:issue author:" + author + " " + created,                           // issuesOpened
		base + " is:pr author:" + author + " " + created,                              // pullRequestsOpened
		base + " is:pr reviewed-by:" + author + " -author:" + author + " " + updated,  // reviewsSubmitted
		base + " is:pr commenter:" + author + " -author:" + author + " " + updated,    // pullRequestsEngaged
		base + " is:issue commenter:" + author + " -author:" + author + " " + updated, // issuesEngaged
	}
}

// toRateLimit adapts the decoded GraphQL node to the exported budget, or nil
// when the response carried no rateLimit block.
func toRateLimit(n *rateLimitNode) *RateLimit {
	if n == nil {
		return nil
	}
	return &RateLimit{Remaining: n.Remaining, ResetAt: n.ResetAt.UTC()}
}

func classifyStatus(code int, hdr http.Header, owner, name string) error {
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return ErrGHNotAuthed
	case code == http.StatusForbidden, code == http.StatusTooManyRequests:
		return parseRateLimited(hdr)
	case code == http.StatusNotFound:
		return fmt.Errorf("%s/%s: %w", owner, name, ErrRepoNotFound)
	default:
		return fmt.Errorf("GitHub API returned status %d for %s/%s", code, owner, name)
	}
}

func classifyGraphQLErrors(errs []gqlError, hdr http.Header, budget *rateLimitNode, owner, name string) error {
	if len(errs) == 0 {
		return nil
	}
	for _, e := range errs {
		switch e.Type {
		case "NOT_FOUND":
			return fmt.Errorf("%s/%s: %w", owner, name, ErrRepoNotFound)
		case "RATE_LIMITED":
			rle := parseRateLimited(hdr)
			// Secondary-limit responses often omit the HTTP rate headers; fall
			// back to the GraphQL budget's resetAt only when the headers gave
			// nothing, so a header signal always wins.
			if rle.ResetAt.IsZero() && rle.RetryAfter == 0 && budget != nil && !budget.ResetAt.IsZero() {
				rle.ResetAt = budget.ResetAt.UTC()
			}
			return rle
		}
	}
	return fmt.Errorf("GitHub GraphQL error for %s/%s: %s", owner, name, errs[0].Message)
}

// parseRateLimited builds the typed rate-limit error from GitHub's response
// headers: X-RateLimit-Reset is a unix epoch (absolute reset); Retry-After is
// either delta-seconds (relative) or an HTTP-date (absolute). Malformed or
// non-positive values are treated as absent — a throttle must never surface as a
// parse error — yielding a zero-value error the caller degrades from. The epoch
// reset is authoritative, so an HTTP-date Retry-After only fills ResetAt when the
// epoch header was absent.
func parseRateLimited(hdr http.Header) RateLimitedError {
	var e RateLimitedError
	if v := strings.TrimSpace(hdr.Get("X-RateLimit-Reset")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			e.ResetAt = time.Unix(n, 0).UTC()
		}
	}
	if v := strings.TrimSpace(hdr.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			// A non-positive delay carries no recoverable signal (zero is the
			// absent value; negative is malformed), so it stays absent.
			if secs > 0 {
				e.RetryAfter = time.Duration(secs) * time.Second
			}
		} else if t, perr := http.ParseTime(v); perr == nil && e.ResetAt.IsZero() {
			e.ResetAt = t.UTC()
		}
	}
	return e
}

func splitOwnerRepo(ownerRepo string) (owner, name string, err error) {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid owner/repo %q: want \"owner/repo\"", ownerRepo)
	}
	return parts[0], parts[1], nil
}

type rateLimitNode struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

// authoredSearchData decodes the root-level authored-activity request: the
// resolved user (nil when the login doesn't exist) and the five aliased search
// counts. Each search node carries only issueCount (first:0 paged no nodes).
type authoredSearchData struct {
	User *struct {
		ID string `json:"id"`
	} `json:"user"`
	S0 searchCount `json:"s0"`
	S1 searchCount `json:"s1"`
	S2 searchCount `json:"s2"`
	S3 searchCount `json:"s3"`
	S4 searchCount `json:"s4"`
}

type searchCount struct {
	IssueCount int `json:"issueCount"`
}

// commitHistoryData decodes the repository-rooted commit-count request.
// defaultBranchRef and target are pointers because GitHub returns null for an
// empty default branch, which leaves the commit count zero.
type commitHistoryData struct {
	DefaultBranchRef *struct {
		Target *struct {
			History struct {
				TotalCount int `json:"totalCount"`
			} `json:"history"`
		} `json:"target"`
	} `json:"defaultBranchRef"`
}

type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type issuesConnection struct {
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []issueNode `json:"nodes"`
}

// activityConnection is the lean issue connection the trajectory fetch decodes:
// pagination plus the create/close/update timestamps, no per-issue payload.
type activityConnection struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []issueActivityNode `json:"nodes"`
}

// issueActivityNode decodes one lean activity node. ClosedAt is a value (not a
// pointer) time: GitHub returns null for an open or reopened issue, and
// time.Time's UnmarshalJSON treats null as a no-op, leaving the zero time — which
// IssueActivity reads as "currently open".
type issueActivityNode struct {
	Number    int       `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	ClosedAt  time.Time `json:"closedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (n issueActivityNode) toActivity() IssueActivity {
	return IssueActivity{Number: n.Number, CreatedAt: n.CreatedAt, ClosedAt: n.ClosedAt}
}

// milestonesConnection is the open-milestone connection: pagination, the
// open-milestone total for the truncation seam, and the per-milestone progress
// nodes.
type milestonesConnection struct {
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []milestoneNode `json:"nodes"`
}

// milestoneNode decodes one open milestone. Open and Closed are the milestone's
// own issue-count connections (the open:/closed: query aliases), so each carries
// only the totalCount that survives the decode contract as a distinct field.
type milestoneNode struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Open        struct {
		TotalCount int `json:"totalCount"`
	} `json:"open"`
	Closed struct {
		TotalCount int `json:"totalCount"`
	} `json:"closed"`
}

func (n milestoneNode) toMilestone() Milestone {
	return Milestone{
		Number:       n.Number,
		Title:        n.Title,
		URL:          n.URL,
		OpenIssues:   n.Open.TotalCount,
		ClosedIssues: n.Closed.TotalCount,
		Description:  n.Description,
	}
}

// milestoneRefNode decodes an issue's milestone association (null when the issue
// is unmilestoned, which leaves the pointer nil).
type milestoneRefNode struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// pullRequestsConnection is the open-pull-request connection: pagination, the
// open-PR total for the truncation seam, and the per-PR nodes.
type pullRequestsConnection struct {
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []pullRequestNode `json:"nodes"`
}

// pullRequestNode decodes one open pull request. statusCheckRollup is a pointer
// because GitHub returns null when the head commit has no checks reported;
// toPullRequest reads its State only when present, leaving CIStatus empty
// otherwise.
type pullRequestNode struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	IsDraft     bool      `json:"isDraft"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	HeadRefName string    `json:"headRefName"`
	Commits     struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

func (n pullRequestNode) toPullRequest() PullRequest {
	ci := ""
	// commits(last:1) returns the head commit; its rollup is the single aggregate
	// CI verdict, or null (left empty) when no checks are reported.
	if len(n.Commits.Nodes) > 0 {
		if r := n.Commits.Nodes[0].Commit.StatusCheckRollup; r != nil {
			ci = r.State
		}
	}
	return PullRequest{
		Number:         n.Number,
		Title:          n.Title,
		URL:            n.URL,
		IsDraft:        n.IsDraft,
		CreatedAt:      n.CreatedAt,
		LastActivityAt: n.UpdatedAt,
		HeadRefName:    n.HeadRefName,
		CIStatus:       ci,
	}
}

type issueNode struct {
	Number    int               `json:"number"`
	Title     string            `json:"title"`
	URL       string            `json:"url"`
	CreatedAt time.Time         `json:"createdAt"`
	BodyText  string            `json:"bodyText"`
	Milestone *milestoneRefNode `json:"milestone"`
	Labels    struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		Nodes []commentNode `json:"nodes"`
	} `json:"comments"`
	TimelineItems struct {
		TotalCount int                 `json:"totalCount"`
		Nodes      []crossRefEventNode `json:"nodes"`
	} `json:"timelineItems"`
}

type commentNode struct {
	CreatedAt time.Time `json:"createdAt"`
	Author    struct {
		TypeName string `json:"__typename"`
		Login    string `json:"login"`
	} `json:"author"`
}

// crossRefEventNode decodes a CrossReferencedEvent. TypeName guards the timeline
// (the itemTypes filter should make every node a CrossReferencedEvent, but a
// defensive check costs nothing). source is the referencing object — an Issue or
// a PullRequest — and only the Issue Number decodes (a PR source leaves it zero).
type crossRefEventNode struct {
	TypeName          string `json:"__typename"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	Source            struct {
		TypeName string `json:"__typename"`
		Number   int    `json:"number"`
	} `json:"source"`
}

func (n issueNode) toIssue() Issue {
	labels := make([]string, 0, len(n.Labels.Nodes))
	for _, l := range n.Labels.Nodes {
		labels = append(labels, l.Name)
	}
	var milestone *MilestoneRef
	if n.Milestone != nil {
		milestone = &MilestoneRef{Number: n.Milestone.Number, Title: n.Milestone.Title}
	}
	return Issue{
		Number:             n.Number,
		Title:              n.Title,
		URL:                n.URL,
		CreatedAt:          n.CreatedAt,
		LastActivityAt:     n.lastHumanActivity(),
		Labels:             labels,
		BodyText:           n.BodyText,
		ReferencedBy:       n.referencedBy(),
		CrossRefsTruncated: n.TimelineItems.TotalCount > len(n.TimelineItems.Nodes),
		Milestone:          milestone,
	}
}

// referencedBy projects the cross-reference timeline to the same-repository issue
// numbers that reference this issue: pull-request and cross-repository sources are
// dropped (the reduction is issue-to-issue within one repo), and the result is
// deduplicated and sorted. GitHub records one event per authored reference, so the
// same source can appear twice (referenced in a body and a comment); the dedup
// keeps a single edge, and the sort makes the decode deterministic.
func (n issueNode) referencedBy() []int {
	seen := make(map[int]struct{})
	for _, e := range n.TimelineItems.Nodes {
		if e.TypeName != "CrossReferencedEvent" || e.IsCrossRepository {
			continue
		}
		if e.Source.TypeName != "Issue" {
			continue
		}
		seen[e.Source.Number] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]int, 0, len(seen))
	for num := range seen {
		out = append(out, num)
	}
	sort.Ints(out)
	return out
}

// lastHumanActivity returns the newest non-bot comment time within the fetched
// window, falling back to the issue's creation when none is present (an all-bot
// or empty thread reads as stale since creation). Comments arrive oldest-first
// (last:25), so the scan runs from the newest backward.
func (n issueNode) lastHumanActivity() time.Time {
	for i := len(n.Comments.Nodes) - 1; i >= 0; i-- {
		c := n.Comments.Nodes[i]
		if !isBot(c.Author.TypeName, c.Author.Login) {
			return c.CreatedAt
		}
	}
	return n.CreatedAt
}

func isBot(typename, login string) bool {
	return typename == "Bot" || strings.HasSuffix(login, "[bot]")
}

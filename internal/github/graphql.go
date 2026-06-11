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
// it, so unread payload doesn't bloat this shared fetch.
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
	for page := 0; page < maxPages; page++ {
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

// do executes one GraphQL request and returns the raw data.repository payload
// plus the rateLimit budget, leaving connection/node decoding to each caller.
// It is the single home for the request, status and GraphQL-error classification,
// and the null-repository check both fetch shapes share. The decoded rateLimit
// node is passed into classifyGraphQLErrors so a RATE_LIMITED error can fall back
// to its resetAt when the response carried no rate headers — the source of the
// throttle recovery signal the server surfaces.
func (f *GraphQLFetcher) do(ctx context.Context, token, query string, vars map[string]any, owner, name string) (repository json.RawMessage, budget *RateLimit, err error) {
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

	var decoded gqlEnvelope
	if decErr := json.NewDecoder(resp.Body).Decode(&decoded); decErr != nil {
		return nil, nil, fmt.Errorf("decoding GitHub response for %s/%s: %w", owner, name, decErr)
	}
	// GraphQL can return a 200 carrying an errors array (e.g. NOT_FOUND with a
	// null repository), so this is checked regardless of HTTP status.
	if gqlErr := classifyGraphQLErrors(decoded.Errors, resp.Header, decoded.Data.RateLimit, owner, name); gqlErr != nil {
		return nil, nil, gqlErr
	}
	if decoded.Data.Repository == nil {
		return nil, nil, fmt.Errorf("%s/%s: %w", owner, name, ErrRepoNotFound)
	}
	return *decoded.Data.Repository, toRateLimit(decoded.Data.RateLimit), nil
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
	for page := 0; page < maxPages; page++ {
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

// gqlEnvelope mirrors the GraphQL envelope: a data block and/or an errors array.
// RateLimit is the root rateLimit field — the remaining points budget and its
// reset, surfaced as a pacing fact on success and harvested as a reset-time
// fallback on a RATE_LIMITED error (where HTTP rate headers are often absent).
// Repository is left as raw JSON so the shared request spine stays agnostic to
// which connection shape (open-issue grooming vs. lean activity) the caller
// decodes from it.
type gqlEnvelope struct {
	Data struct {
		RateLimit  *rateLimitNode   `json:"rateLimit"`
		Repository *json.RawMessage `json:"repository"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

type rateLimitNode struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
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

type issueNode struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
	BodyText  string    `json:"bodyText"`
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

// Package server builds the overstory MCP server: a manifest-driven,
// project-management server that reduces a repository's issue and PR landscape
// to compact structured facts and leaves rendering to the caller.
//
// The split of responsibility is deliberate and load-bearing: this server is
// pure mechanism — it fetches, computes, and reduces. Deciding how to present
// the result, and which narrative to wrap it in, is the calling agent's job.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jakewan/overstory/internal/authored"
	"github.com/jakewan/overstory/internal/backlog"
	"github.com/jakewan/overstory/internal/criticalpath"
	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/maintenance"
	"github.com/jakewan/overstory/internal/manifest"
	"github.com/jakewan/overstory/internal/reduce"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// serverName is the programmatic MCP identifier (lowercase, matches the
	// binary and config key); serverTitle is the human-readable display name MCP
	// clients show. The split mirrors the MCP registry's name/title convention.
	serverName    = "overstory"
	serverTitle   = "Overstory"
	serverVersion = "0.1.0"
)

// config holds the server's resolved dependencies. Options override the
// production defaults; tests inject fakes for hermetic coverage.
type config struct {
	fetcher       github.Fetcher
	manifestRoot  string
	manifestFiles []string
	now           func() time.Time
}

// Option configures the server's dependencies.
type Option func(*config)

// WithFetcher overrides the GitHub fetcher — issues, milestones, and pull
// requests (tests inject a fake).
func WithFetcher(f github.Fetcher) Option {
	return func(c *config) { c.fetcher = f }
}

// WithManifestRoot overrides the manifests.d discovery directory.
func WithManifestRoot(dir string) Option {
	return func(c *config) { c.manifestRoot = dir }
}

// WithManifestFiles overrides discovery with an explicit ordered file list,
// taking precedence over the directory.
func WithManifestFiles(files []string) Option {
	return func(c *config) { c.manifestFiles = files }
}

// WithClock overrides the wall clock used to measure staleness (tests inject a
// fixed time for determinism).
func WithClock(now func() time.Time) Option {
	return func(c *config) { c.now = now }
}

// New builds the overstory MCP server and registers the backlog_review,
// project_summary, milestone_tracks, authored_activity, authored_activity_batch,
// maintenance_activity, and maintenance_activity_batch tools.
// With no options it uses production defaults: issues fetched via the GitHub
// GraphQL API (credentials from gh), manifests discovered from
// $XDG_CONFIG_HOME/overstory/manifests.d (or OVERSTORY_MANIFESTS), and the real
// wall clock. This is the one place process environment is read.
func New(opts ...Option) *mcp.Server {
	cfg := config{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.fetcher == nil {
		cfg.fetcher = github.NewGraphQLFetcher()
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}

	root, files := cfg.manifestRoot, cfg.manifestFiles
	if root == "" && len(files) == 0 {
		if files = manifestFilesFromEnv(); len(files) == 0 {
			root = defaultManifestRoot()
		}
	}
	resolver := manifest.NewResolver(root, files)

	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Title: serverTitle, Version: serverVersion}, nil)
	mcp.AddTool(srv, backlogReviewTool(), backlogReviewHandler(resolver, cfg.fetcher, cfg.now))
	mcp.AddTool(srv, projectSummaryTool(), projectSummaryHandler(resolver, cfg.fetcher, cfg.now))
	mcp.AddTool(srv, milestoneTracksTool(), milestoneTracksHandler(resolver, cfg.fetcher, cfg.now))
	mcp.AddTool(srv, authoredActivityTool(), authoredActivityHandler(cfg.fetcher, cfg.now))
	mcp.AddTool(srv, authoredActivityBatchTool(), authoredActivityBatchHandler(cfg.fetcher, cfg.now, authoredBatchConcurrency, authoredBatchPerRepoTimeout))
	mcp.AddTool(srv, maintenanceActivityTool(), maintenanceActivityHandler(cfg.fetcher, cfg.now))
	mcp.AddTool(srv, maintenanceActivityBatchTool(), maintenanceActivityBatchHandler(cfg.fetcher, cfg.now, maintenanceBatchConcurrency, maintenanceBatchPerRepoTimeout))
	return srv
}

// manifestFilesFromEnv parses OVERSTORY_MANIFESTS as a colon-separated file
// list, treating empty-after-trim as unset.
func manifestFilesFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("OVERSTORY_MANIFESTS"))
	if raw == "" {
		return nil
	}
	var files []string
	for p := range strings.SplitSeq(raw, ":") {
		if p = strings.TrimSpace(p); p != "" {
			files = append(files, p)
		}
	}
	return files
}

// defaultManifestRoot resolves the XDG drop-in directory, falling back to
// ~/.config when XDG_CONFIG_HOME is unset. An empty result yields generic
// defaults rather than an error.
func defaultManifestRoot() string {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "overstory", "manifests.d")
}

// backlogReviewInput is the tool's decoded input. Constraints (required fields,
// limit default and bounds) live in the published schema, not here.
type backlogReviewInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Limit int    `json:"limit"`
}

// backlogReviewTool publishes the input contract via a hand-written schema. The
// installed jsonschema-go infers neither defaults nor bounds from struct tags
// (and would mark every field required), so the schema is written explicitly:
// owner/repo required, limit optional with a default and 1..100 bounds applied
// by the SDK before the handler runs.
func backlogReviewTool() *mcp.Tool {
	minLimit, maxLimit := 1.0, 100.0
	return &mcp.Tool{
		Name:        "backlog_review",
		Description: "Survey a GitHub repository's open-issue backlog and return compact structured facts for the caller to render: a staleness block (exact open count, inactivity-band counts, the stalest issues), a deferred-review block (open issues carrying the repo's manifest-declared deferred labels), an area-balance block (the issue distribution across the repo's functional areas, identified by manifest-declared labels and prefixes), a quality block (open issues with a too-thin body, no labels, or — when configured — a missing required-label category), an overlap block (groups of open issues with similar titles — candidate duplicates — found over the fetched window), a cross-reference block (groups of open issues that reference one another issue-to-issue via GitHub cross-references — candidate consolidation — found over the fetched window), a trajectory block (for each manifest-declared lookback window in days, the issues created, closed, and net created-minus-closed — the backlog growing/shrinking signal — over a second open-and-closed fetch; this block is aggregate and not affected by limit, and marks itself unavailable if that fetch fails rather than failing the whole review), and a critical-path block (when the repo's manifest declares an ordered stream list and a critical-path label: each declared stream in order, its open critical-path-labeled issue members, and a per-stream gate-cleared signal — cleared meaning no open critical-path issue remains in the stream, provisional when the fetch window is truncated; absent the convention the block reports itself not configured), and an open-issue-set block (the ascending, distinct set of open issue numbers in the fetched window — the resolvable surface for a deferred issue's stated bodyRefs, so a caller can tell a ref naming a live open issue in this repo from one that does not; same-repo, open, issues-only, and the full window never capped by limit, with a fetchTruncated flag marking when the set is a floor — presence names a live open issue, absence is not proof of resolution, since the ref may be a closed issue, an open PR, a cross-repo reference, or beyond a truncated window).",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner": {Type: "string", Description: "repository owner (user or org)"},
				"repo":  {Type: "string", Description: "repository name"},
				"limit": {
					Type:        "integer",
					Description: "maximum number of items to list per reduction: issues for staleness, deferred, and quality; overlap groups for overlap; cross-reference groups for crossRef; members per stream for criticalPath",
					Default:     json.RawMessage("20"),
					Minimum:     &minLimit,
					Maximum:     &maxLimit,
				},
			},
			Required: []string{"owner", "repo"},
		},
	}
}

// backlogReviewHandler resolves the repo's conventions, fetches its open issues,
// and reduces them to the composite backlog facts — one block per grooming
// signal (staleness, deferred, area balance, quality, overlap, cross-reference,
// trajectory, critical path). Most blocks reduce the one open-issue fetch; trajectory adds a
// second open-and-closed fetch and degrades to an unavailable block on failure.
// Errors from the open fetch are returned plain so the SDK surfaces them as tool
// errors (IsError); a manifest error names a file, so it is logged to stderr and
// replaced with a repo-named message on the caller channel.
func backlogReviewHandler(resolver *manifest.Resolver, fetcher github.Fetcher, now func() time.Time) mcp.ToolHandlerFor[backlogReviewInput, backlog.Facts] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in backlogReviewInput) (*mcp.CallToolResult, backlog.Facts, error) {
		owner, repo := strings.TrimSpace(in.Owner), strings.TrimSpace(in.Repo)
		if owner == "" || repo == "" {
			return nil, backlog.Facts{}, fmt.Errorf("owner and repo are required")
		}
		ownerRepo := owner + "/" + repo

		cfg, matched, err := resolver.Resolve(ownerRepo)
		if err != nil {
			log.Printf("overstory: manifest resolution for %s: %v", ownerRepo, err)
			return nil, backlog.Facts{}, fmt.Errorf("manifest configuration error for %s", ownerRepo)
		}

		result, err := fetcher.ListOpenIssues(ctx, ownerRepo, cfg.Staleness.FetchLimit)
		if err != nil {
			// A throttle carries a recovery signal the caller can act on: name the
			// absolute instant it can retry at, resolving a relative retry-after
			// against this layer's clock. Other failures surface plain.
			if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
				if when := rateLimitResetTime(rle, now); !when.IsZero() {
					return nil, backlog.Facts{}, fmt.Errorf("fetching issues for %s: %w (retry after %s)", ownerRepo, err, when.UTC().Format(time.RFC3339))
				}
			}
			return nil, backlog.Facts{}, fmt.Errorf("fetching issues for %s: %w", ownerRepo, err)
		}

		// Bind the clock once so every block shares one generation time; the two
		// reductions run over the same fetched window.
		n := now()
		// Build the staleness exclusion set over the full fetched window using the
		// same deferred-label matching ReduceDeferred applies, so staleness counts
		// only neglected work while deferred still surfaces the parked issues.
		deferredNums := backlog.DeferredNumbers(result.Issues, cfg.Deferred.Labels)
		staleness := backlog.ReduceStaleness(result.Issues, result.TotalOpen, cfg.Staleness.ThresholdDays, in.Limit, deferredNums, n)
		staleness.FetchLimit = cfg.Staleness.FetchLimit
		staleness.ThresholdSource = thresholdSource(matched)
		deferred := backlog.ReduceDeferred(result.Issues, result.TotalOpen, cfg.Deferred.Labels, in.Limit, n)
		area := backlog.ReduceAreaBalance(result.Issues, result.TotalOpen, cfg.AreaBalance.Labels, mapPrefixes(cfg.AreaBalance.Prefixes))
		quality := backlog.ReduceQuality(result.Issues, result.TotalOpen, mapQuality(cfg.Quality), in.Limit, n)
		overlap := backlog.ReduceOverlap(result.Issues, result.TotalOpen, backlog.OverlapParams{TitleThreshold: cfg.Overlap.TitleSimilarityThreshold}, in.Limit)
		crossref := backlog.ReduceCrossRef(result.Issues, result.TotalOpen, in.Limit)
		criticalPath := criticalpath.Reduce(result.Issues, result.TotalOpen, criticalpath.Params{
			Streams:      cfg.CriticalPath.Streams,
			Label:        cfg.CriticalPath.Label,
			AreaLabels:   cfg.AreaBalance.Labels,
			AreaPrefixes: mapPrefixes(cfg.AreaBalance.Prefixes),
		}, in.Limit)

		// Trajectory needs a second fetch (closed issues too); a failure there
		// degrades the block rather than failing the whole review, since the other
		// six blocks already reduced the successful open-issue fetch.
		trajectory, budget := reduceTrajectory(ctx, fetcher, ownerRepo, cfg.Trajectory, result.RateLimit, n, now)

		return nil, backlog.Facts{
			Repo:         ownerRepo,
			GeneratedAt:  n,
			Staleness:    staleness,
			Deferred:     deferred,
			AreaBalance:  area,
			Quality:      quality,
			Overlap:      overlap,
			CrossRef:     crossref,
			Trajectory:   trajectory,
			CriticalPath: criticalPath,
			// The full fetched open-issue window, never capped by in.Limit: a caller
			// resolves a deferred issue's bodyRefs against this set, so a real open
			// blocker beyond any list cap must still appear here.
			OpenIssueSet: reduce.NewOpenIssueSet(openIssueNumbers(result.Issues), len(result.Issues) < result.TotalOpen),
			RateLimit:    mapRateLimit(budget),
		}, nil
	}
}

// reduceTrajectory runs the trajectory reduction's own fetch — issues updated
// since the widest window, open and closed — and reduces it, degrading to an
// unavailable block on failure rather than failing the whole review. It also
// returns the rate-limit budget to surface: the trajectory fetch's fresher budget
// on success; on a rate-limit degrade, the throttle's recovery signal (Remaining
// 0 plus its reset) rather than the now-stale open-fetch budget that would tell a
// caller "you have budget" at the moment it was throttled; on any other degrade,
// the open fetch's budget (the last successful read).
func reduceTrajectory(ctx context.Context, fetcher github.Fetcher, ownerRepo string, cfg manifest.TrajectoryConfig, openBudget *github.RateLimit, n time.Time, now func() time.Time) (backlog.TrajectoryFacts, *github.RateLimit) {
	since := n.UTC().AddDate(0, 0, -maxInt(cfg.Windows))
	activity, err := fetcher.ListIssuesUpdatedSince(ctx, ownerRepo, since, cfg.FetchLimit)
	if err == nil {
		return backlog.ReduceTrajectory(activity.Activities, cfg.Windows, activity.Truncated, n), freshestBudget(openBudget, activity.RateLimit)
	}
	if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
		return backlog.TrajectoryFacts{Available: false, Unavailable: "rate_limited", Windows: []backlog.TrajectoryWindow{}},
			&github.RateLimit{Remaining: 0, ResetAt: rateLimitResetTime(rle, now)}
	}
	// The cause may name internal detail; keep it on stderr, off the caller channel.
	log.Printf("overstory: trajectory fetch for %s: %v", ownerRepo, err)
	return backlog.TrajectoryFacts{Available: false, Unavailable: "fetch_failed", Windows: []backlog.TrajectoryWindow{}}, openBudget
}

// maxInt returns the largest of xs. Trajectory windows are validated non-empty
// and positive (manifest.validate), so the zero default is unreachable in
// practice; the guard keeps a future caller from computing a garbage window.
func maxInt(xs []int) int {
	m := 0
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

// freshestBudget picks the budget to report across the two fetches: the
// trajectory fetch is the later observation, so its budget wins when present; a
// nil (that fetch carried no budget) falls back to the open fetch's.
func freshestBudget(open, trajectory *github.RateLimit) *github.RateLimit {
	if trajectory != nil {
		return trajectory
	}
	return open
}

// rateLimitResetTime resolves a throttle's recovery signal to an absolute
// wall-clock instant: an absolute ResetAt verbatim, else now()+RetryAfter, else
// zero. A resolved time at or before now is clamped to zero (clock skew, or a
// stale signal) so the caller is never told to retry at an already-elapsed
// instant — this is the one place the server's clock validates the github-parsed
// time the clock-free fetch layer cannot.
func rateLimitResetTime(e github.RateLimitedError, now func() time.Time) time.Time {
	// Sample the clock once so the retry-after resolution and the past-time clamp
	// reason about the same instant.
	n := now()
	var when time.Time
	switch {
	case !e.ResetAt.IsZero():
		when = e.ResetAt
	case e.RetryAfter > 0:
		when = n.Add(e.RetryAfter)
	default:
		return time.Time{}
	}
	if !when.After(n) {
		return time.Time{}
	}
	return when
}

// mapRateLimit adapts the fetch's budget snapshot to the shared rate-limit fact
// both tools' outputs embed, keeping the reduction layer decoupled from the github
// layer; nil (no budget observed) passes through so the fact is omitted from the
// output.
func mapRateLimit(in *github.RateLimit) *reduce.RateLimitFacts {
	if in == nil {
		return nil
	}
	return &reduce.RateLimitFacts{Remaining: in.Remaining, ResetAt: in.ResetAt}
}

// openIssueNumbers extracts the issue numbers from a fetched open-issue window, so
// the handler can build the open-issue set without the reduce layer importing the
// github shape (the same decoupling mapRateLimit/mapPrefixes keep). NewOpenIssueSet
// sorts and dedupes, so the order here is irrelevant.
func openIssueNumbers(issues []github.Issue) []int {
	nums := make([]int, 0, len(issues))
	for _, is := range issues {
		nums = append(nums, is.Number)
	}
	return nums
}

func thresholdSource(matched bool) string {
	if matched {
		return "manifest"
	}
	return "default"
}

// mapPrefixes adapts the manifest's prefix rules to the backlog matcher's, so the
// reduction layer stays decoupled from the convention-resolution layer.
func mapPrefixes(in []manifest.PrefixRule) []reduce.PrefixRule {
	out := make([]reduce.PrefixRule, len(in))
	for i, p := range in {
		out[i] = reduce.PrefixRule{Prefix: p.Prefix, Delimiter: p.Delimiter}
	}
	return out
}

// mapQuality adapts the manifest's quality convention to the backlog reduction's
// params, keeping the same layer decoupling as mapPrefixes.
func mapQuality(in manifest.QualityConfig) backlog.QualityParams {
	cats := make([]backlog.Category, len(in.RequiredCategories))
	for i, c := range in.RequiredCategories {
		cats[i] = backlog.Category{Name: c.Name, Labels: c.Labels, Prefixes: mapPrefixes(c.Prefixes)}
	}
	return backlog.QualityParams{MinBodyLength: in.MinBodyLength, Categories: cats}
}

// authoredActivityInput is the tool's decoded input. Unlike the other tools it
// carries an author login and an explicit window, and reads no manifest
// conventions. Since/Until are RFC3339 timestamps; Until is optional and defaults
// to now. Required fields live in the published schema, not here.
type authoredActivityInput struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Author string `json:"author"`
	Since  string `json:"since"`
	Until  string `json:"until"`
}

// authoredActivityTool publishes the input contract. owner/repo/author/since are
// required; until is optional (defaults to now). There is no limit parameter —
// the tool returns counts, not lists, so there is nothing to cap.
func authoredActivityTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "authored_activity",
		Description: "Measure how much one GitHub user authored and engaged with in one repository over a caller-supplied time window, and return six decomposed, objective counts for the caller to weigh and render: commitsAuthored (default-branch commits attributed to the author's linked identity — misses squash-merged and email-unlinked commits), issuesOpened and pullRequestsOpened (items the author created in the window), reviewsSubmitted (others' pull requests the author reviewed — peer review, excluding the author's own PRs), and pullRequestsEngaged and issuesEngaged (items the author commented on but did not author). Each count ships with a per-category fidelity label, because the categories are not equally precise: the commit count is event-precise within its attribution limits, while the five search-derived counts are search-index-approximate and (for reviews and engagement) windowed by the item's activity rather than the exact comment/review date. The counts are never summed — weighting and the attention verdict stay caller-side. This tool is author- and window-driven and reads no manifest conventions; it inherits the operator's gh credentials, so it can measure private repositories the user-rooted contributions query cannot. It runs over a single owner/repo per call (the caller loops to measure several). An unresolved author login is a named error, never six silent zeros; any fetch failure surfaces as a tool error rather than a partial count.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner":  {Type: "string", Description: "repository owner (user or org)"},
				"repo":   {Type: "string", Description: "repository name"},
				"author": {Type: "string", Description: "the GitHub login whose authored and engagement activity is measured"},
				"since":  {Type: "string", Description: "window start as an RFC3339 timestamp (e.g. 2026-01-01T00:00:00Z)"},
				"until":  {Type: "string", Description: "window end as an RFC3339 timestamp; defaults to now when omitted"},
			},
			Required: []string{"owner", "repo", "author", "since"},
		},
	}
}

// authoredActivityHandler validates the window and author, fetches the decomposed
// authored-activity counts, and reduces them to facts. It reads no manifest (the
// primitive is window/author-driven, not convention-driven), so unlike the other
// handlers it takes no resolver. The window is parsed and ordered before any
// fetch, so a malformed or inverted window fails fast with a named error; a
// throttle surfaces its retry instant like the other tools.
func authoredActivityHandler(fetcher github.Fetcher, now func() time.Time) mcp.ToolHandlerFor[authoredActivityInput, authored.Facts] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in authoredActivityInput) (*mcp.CallToolResult, authored.Facts, error) {
		owner, repo := strings.TrimSpace(in.Owner), strings.TrimSpace(in.Repo)
		author := strings.TrimSpace(in.Author)
		if owner == "" || repo == "" {
			return nil, authored.Facts{}, fmt.Errorf("owner and repo are required")
		}
		if author == "" {
			return nil, authored.Facts{}, fmt.Errorf("author is required")
		}
		ownerRepo := owner + "/" + repo

		n := now()
		since, until, werr := parseWindow(in.Since, in.Until, n)
		if werr != nil {
			return nil, authored.Facts{}, werr
		}

		result, err := fetcher.AuthoredActivity(ctx, ownerRepo, author, since, until)
		if err != nil {
			// A throttle carries a retry instant the caller can act on; other
			// failures (including an unresolved author) surface plain.
			if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
				if when := rateLimitResetTime(rle, now); !when.IsZero() {
					return nil, authored.Facts{}, fmt.Errorf("fetching authored activity for %s: %w (retry after %s)", ownerRepo, err, when.UTC().Format(time.RFC3339))
				}
			}
			return nil, authored.Facts{}, fmt.Errorf("fetching authored activity for %s: %w", ownerRepo, err)
		}

		facts := authored.Reduce(result, author, since, until)
		facts.Repo = ownerRepo
		facts.GeneratedAt = n
		facts.RateLimit = mapRateLimit(result.RateLimit)
		return nil, facts, nil
	}
}

// parseWindow parses the since/until RFC3339 inputs into an ordered window: an
// omitted until defaults to now, and an unparseable, inverted, or empty window is
// rejected with a named error. The caller samples now once and passes it in so the
// whole call — and, for a batch, every repo — shares one window instant.
func parseWindow(since, until string, now time.Time) (time.Time, time.Time, error) {
	s, err := time.Parse(time.RFC3339, strings.TrimSpace(since))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("since must be an RFC3339 timestamp, got %q", since)
	}
	u := now
	if raw := strings.TrimSpace(until); raw != "" {
		if u, err = time.Parse(time.RFC3339, raw); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("until must be an RFC3339 timestamp, got %q", until)
		}
	}
	if !u.After(s) {
		return time.Time{}, time.Time{}, fmt.Errorf("until (%s) must be after since (%s)", u.UTC().Format(time.RFC3339), s.UTC().Format(time.RFC3339))
	}
	return s, u, nil
}

const (
	// authoredBatchConcurrency bounds how many repos fetch at once. It is
	// deliberately small: each repo is ~2 GraphQL requests, several of them search
	// operations, and GitHub's secondary rate limit is sensitive to concurrent
	// bursts on the search endpoint, not just the primary points budget.
	authoredBatchConcurrency = 3
	// authoredBatchMaxRepos caps a single batch so concurrency × per-repo ops stays
	// bounded; it mirrors the schema's maxItems.
	authoredBatchMaxRepos = 50
	// authoredBatchPerRepoTimeout bounds one repo's whole fetch (both sequential
	// GraphQL requests together), so a single hung or pathological repo cannot hold a
	// concurrency slot for the full ~60s the per-request transport timeout would
	// otherwise allow (2 × 30s). It is derived per repo from the batch context, so a
	// repo that trips it degrades to its own fetch_failed marker without touching the
	// batch — a healthy repo returns far inside this budget.
	authoredBatchPerRepoTimeout = 45 * time.Second
)

// authoredActivityBatchInput is the batch tool's decoded input: a list of
// owner/repo slugs measured for one author over one window. Until is optional
// (defaults to now); constraints live in the published schema.
type authoredActivityBatchInput struct {
	Repos  []string `json:"repos"`
	Author string   `json:"author"`
	Since  string   `json:"since"`
	Until  string   `json:"until"`
}

// authoredActivityBatchTool publishes the batch input contract. repos is a
// non-empty, bounded array of owner/repo; author/since are required; until is
// optional. There is no limit parameter — the tool returns per-repo counts, not
// lists.
func authoredActivityBatchTool() *mcp.Tool {
	minRepos, maxRepos := 1, authoredBatchMaxRepos
	return &mcp.Tool{
		Name:        "authored_activity_batch",
		Description: "Measure how much one GitHub user authored and engaged with across several repositories over a caller-supplied time window — the batched form of authored_activity. Given a list of owner/repo and one author login, it fans out the same six decomposed counts per repository (commitsAuthored, issuesOpened, pullRequestsOpened, reviewsSubmitted, pullRequestsEngaged, issuesEngaged — each with its per-category fidelity label) and returns one entry per repository in request order. Repositories are independent — each entry is either the counts or one of four per-repo markers: not_found, rate_limited, fetch_failed, not_attempted. A not_found or fetch_failed repository degrades to its own marker without sinking the others' counts, and the batch surfaces a single aggregated rate-limit budget — the tightest remaining across the successful repositories, or a throttle's reset instant when any repository was throttled. A rate_limited repository additionally trips backpressure: to avoid amplifying the rate limit the batch stops launching new fetches, so an arbitrary subset of the not-yet-started repositories returns not_attempted (a deliberate skip, not a failure — in-flight fetches still complete); this can pre-empt the whole-batch author error below when a throttle precedes the author's resolution. Because the author login resolves globally (independent of repository), an unresolvable login is one whole-batch error naming it rather than per-repo markers — but a login that resolves to a real, unrelated account yields honest zeros that no tool can distinguish from genuine inactivity. The tool reads no manifest conventions and inherits the operator's gh credentials, so it can measure private repositories. It returns per-repo facts only — summing across repositories, ranking, and the attention verdict stay caller-side.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"repos": {
					Type:        "array",
					Description: "the repositories to measure, each as an owner/repo slug",
					Items:       &jsonschema.Schema{Type: "string"},
					MinItems:    &minRepos,
					MaxItems:    &maxRepos,
				},
				"author": {Type: "string", Description: "the GitHub login whose authored and engagement activity is measured"},
				"since":  {Type: "string", Description: "window start as an RFC3339 timestamp (e.g. 2026-01-01T00:00:00Z)"},
				"until":  {Type: "string", Description: "window end as an RFC3339 timestamp; defaults to now when omitted"},
			},
			Required: []string{"repos", "author", "since"},
		},
	}
}

// authoredActivityBatchHandler validates the author, the repo list, and the
// window, fans out the per-repo fetches, and reduces them to batch facts. It reads
// no manifest (the primitive is window/author-driven) so it takes no resolver. An
// unresolvable author — repo-independent, so it fails every repo — surfaces as one
// whole-batch error; every other failure degrades only its own repo's entry.
func authoredActivityBatchHandler(fetcher github.Fetcher, now func() time.Time, concurrency int, perRepoTimeout time.Duration) mcp.ToolHandlerFor[authoredActivityBatchInput, authored.BatchFacts] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in authoredActivityBatchInput) (*mcp.CallToolResult, authored.BatchFacts, error) {
		author := strings.TrimSpace(in.Author)
		if author == "" {
			return nil, authored.BatchFacts{}, fmt.Errorf("author is required")
		}
		repos, verr := validateRepos(in.Repos, authoredBatchMaxRepos)
		if verr != nil {
			return nil, authored.BatchFacts{}, verr
		}
		n := now()
		since, until, werr := parseWindow(in.Since, in.Until, n)
		if werr != nil {
			return nil, authored.BatchFacts{}, werr
		}

		entries := fanOutAuthored(ctx, fetcher, repos, author, since, until, now, concurrency, perRepoTimeout)
		// A cancelled request must surface as an error, not a fabricated success: the
		// fan-out stamps not-yet-started repos with a placeholder, so without this
		// guard the handler would return a 200 result built from those placeholders.
		if cerr := ctx.Err(); cerr != nil {
			return nil, authored.BatchFacts{}, fmt.Errorf("authored_activity_batch cancelled: %w", cerr)
		}
		// The author login is repo-independent, so an unresolvable login fails every
		// repo identically — escalate it to one named whole-batch error rather than
		// returning N silent author-not-found markers.
		for _, e := range entries {
			if e.Unavailable == authored.UnavailableAuthorNotFound {
				return nil, authored.BatchFacts{}, fmt.Errorf("author %q is not a GitHub user", author)
			}
		}

		facts := authored.ReduceBatch(entries, author, since, until)
		facts.GeneratedAt = n
		return nil, facts, nil
	}
}

// validateRepos normalizes and validates the batch's repo slugs before any fetch:
// non-empty, within maxRepos, each exactly owner/repo (one slash, both halves
// non-blank), and no case-insensitive duplicates (matching the manifest layer's
// repo-key collision rule). It returns the canonicalized slugs in input order. The
// cap is a parameter so the authored and maintenance batches can share the helper
// while each names its own bound (they coincide today, but the coupling is removed).
func validateRepos(in []string, maxRepos int) ([]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("repos must list at least one owner/repo")
	}
	if len(in) > maxRepos {
		return nil, fmt.Errorf("repos lists %d repositories, at most %d allowed", len(in), maxRepos)
	}
	repos := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		owner, name, ok := strings.Cut(strings.TrimSpace(raw), "/")
		owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("repository %q is not a valid owner/repo", raw)
		}
		slug := owner + "/" + name
		key := strings.ToLower(slug)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("repository %q is listed more than once", raw)
		}
		seen[key] = struct{}{}
		repos = append(repos, slug)
	}
	return repos, nil
}

// fanOutAuthored fetches each repo's authored activity concurrently (bounded by
// concurrency) and classifies every outcome into a BatchEntry, so one repo's failure
// degrades only its own entry. Each goroutine writes its own index in a pre-sized
// slice (distinct indices, read only after Wait), preserving input order without a
// mutex. The request ctx threads to every fetch so a client cancellation aborts
// in-flight HTTP; a goroutine blocked acquiring the semaphore abandons its slot when
// the batch is cancelled, and one that acquires after cancellation skips its fetch
// (fetchAuthoredEntry's fast-path ctx check). The per-entry markers a cancelled batch
// produces are placeholders the handler discards wholesale once it observes
// ctx.Err().
//
// Two adverse-condition adaptations layer on top: a throttle on any repo trips
// stopLaunch so not-yet-started repos are skipped as not_attempted rather than
// amplifying the rate limit (see below), and each fetch carries a perRepoTimeout
// deadline so a hung repo degrades to its own fetch_failed without stalling the rest.
func fanOutAuthored(ctx context.Context, fetcher github.Fetcher, repos []string, author string, since, until time.Time, now func() time.Time, concurrency int, perRepoTimeout time.Duration) []authored.BatchEntry {
	entries := make([]authored.BatchEntry, len(repos))
	// Guard two preconditions on the tuning parameters, so a future caller
	// misconfiguring an otherwise-trusted internal parameter degrades to a safe
	// default rather than a surprising failure. Production wires positive consts.
	// concurrency must be at least 1: a zero buffer makes an unbuffered semaphore
	// (every send blocks forever, hanging the handler) and a negative one panics at
	// make. A non-positive perRepoTimeout would make context.WithTimeout fire an
	// immediate deadline, degrading every repo to fetch_failed; fall back to the
	// default budget instead.
	if concurrency < 1 {
		concurrency = 1
	}
	if perRepoTimeout <= 0 {
		perRepoTimeout = authoredBatchPerRepoTimeout
	}
	sem := make(chan struct{}, concurrency)
	// stopLaunch is the throttle backpressure signal: once any repo's fetch returns a
	// rate limit, the batch stops launching new fetches rather than feeding the very
	// secondary-rate-limit it just hit. It is an atomic flag, not context
	// cancellation, deliberately: in-flight fetches run on the parent ctx and must be
	// allowed to complete — cancelling a shared ctx would abort them into fetch_failed.
	var stopLaunch atomic.Bool
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				entries[i] = authored.BatchEntry{Repo: repo, Unavailable: authored.UnavailableFetchFailed}
				return
			}
			// Backpressure gate, checked after acquiring the slot: if an earlier repo
			// already throttled, skip this fetch entirely (no network call, no per-repo
			// timeout context) and record it as a deliberate not_attempted skip.
			if stopLaunch.Load() {
				entries[i] = authored.BatchEntry{Repo: repo, Unavailable: authored.UnavailableNotAttempted}
				return
			}
			entry := fetchAuthoredEntry(ctx, fetcher, repo, author, since, until, now, perRepoTimeout)
			if entry.Unavailable == authored.UnavailableRateLimited {
				stopLaunch.Store(true)
			}
			entries[i] = entry
		})
	}
	wg.Wait()
	return entries
}

// fetchAuthoredEntry fetches one repo under a perRepoTimeout deadline (derived from
// ctx, so a hung repo can't hold its slot for the full transport timeout) and maps
// the outcome to a BatchEntry: counts on success, else a per-repo marker. An
// unresolvable author is flagged with the internal author-not-found reason the
// handler escalates to a whole-batch error; a throttle carries its resolved reset
// instant; any other failure — including a deadline trip, which classifies as no
// sentinel — is fetch_failed, its cause logged to stderr (never the caller channel),
// as the trajectory fetch degrade does.
func fetchAuthoredEntry(ctx context.Context, fetcher github.Fetcher, repo, author string, since, until time.Time, now func() time.Time, perRepoTimeout time.Duration) authored.BatchEntry {
	// On an already-cancelled batch, skip the network call: the handler discards
	// the whole result on ctx.Err(), so this placeholder is never returned. This
	// checks the parent ctx before deriving the per-repo deadline below, so a client
	// cancellation is distinguished from this repo's own timeout.
	if ctx.Err() != nil {
		return authored.BatchEntry{Repo: repo, Unavailable: authored.UnavailableFetchFailed}
	}
	// Bound this one repo's whole fetch (both sequential requests) with a deadline
	// derived from the batch ctx, so a hung repo releases its slot instead of holding
	// it for the full transport timeout. A trip leaves the parent ctx alive, so it
	// surfaces as this repo's fetch_failed (the DeadlineExceeded matches no sentinel
	// and is not a RateLimitedError) without failing the batch.
	ctx, cancel := context.WithTimeout(ctx, perRepoTimeout)
	defer cancel()
	result, err := fetcher.AuthoredActivity(ctx, repo, author, since, until)
	if err == nil {
		return authored.BatchEntry{Repo: repo, Result: result}
	}
	switch {
	case errors.Is(err, github.ErrAuthorNotFound):
		return authored.BatchEntry{Repo: repo, Unavailable: authored.UnavailableAuthorNotFound}
	case errors.Is(err, github.ErrRepoNotFound):
		return authored.BatchEntry{Repo: repo, Unavailable: authored.UnavailableNotFound}
	}
	if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
		return authored.BatchEntry{Repo: repo, Unavailable: authored.UnavailableRateLimited, ResetAt: rateLimitResetTime(rle, now)}
	}
	log.Printf("overstory: authored activity fetch for %s: %v", repo, err)
	return authored.BatchEntry{Repo: repo, Unavailable: authored.UnavailableFetchFailed}
}

// maintenanceFetchLimit bounds one repo's issue-events scan. The REST stream is
// newest-first with no server-side window filter, so the fetch reads back to the
// window floor up to this cap; a busy repo that hits the cap before crossing the
// floor reports Truncated, surfacing the lower-bound coverage rather than hiding
// it. It is sized to cover a normal grooming window comfortably while bounding the
// REST round trips (each page is up to 100 events).
const maintenanceFetchLimit = 500

// maintenanceActivityInput is the tool's decoded input: an owner/repo, the actor
// login whose state mutations are measured, and an explicit window. It reads no
// manifest. Since/Until are RFC3339; Until is optional and defaults to now.
type maintenanceActivityInput struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Author string `json:"author"`
	Since  string `json:"since"`
	Until  string `json:"until"`
}

// maintenanceActivityTool publishes the input contract. owner/repo/author/since
// are required; until is optional (defaults to now). There is no limit parameter —
// the fetch scans to the window floor under an internal cap, surfaced via the
// truncated flag.
func maintenanceActivityTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "maintenance_activity",
		Description: "Measure the state-mutation maintenance one GitHub user paid to existing issues and pull requests in one repository over a caller-supplied time window — the grooming attention authored_activity structurally misses (a relabeling, milestoning, deferral-labeling, closing/reopening, assigning, and renaming afternoon produces near-zero authored counts). It returns the touched items, most-recently-touched first, each carrying the actor's qualifying mutations in chronological order: an event's type, instant, and per-type payload (the label name, milestone title, assignee login, or rename before/after). Each item flags isPullRequest, because the events stream mixes issues and pull requests (roughly a third are PR events) — the tool stays tag-blind and lets the caller split the mix. Each event flags viaAutomation (set when GitHub attributes it to an app), so a caller can exclude workflow/app-driven churn — with the blind spot that an automation acting as the measured login is still attributed to that login, so the flag, not the count, carries the meaning. The actor is matched by login string against the events stream, so an unknown or inactive login yields zero items, never an error (unlike authored_activity's author resolution). The truncated flag marks a window the fetch could not fully cover (a busy repo past the internal scan cap), so a recent mutation may be missing. The rateLimit budget is the REST core pool (requests per hour) — a different pool from authored_activity's GraphQL points, so the two budgets are not comparable. The window's upper bound is applied after the fetch; a window ending far in the past over-reads from now and can report truncated with no in-window items. This tool is actor- and window-driven, reads no manifest conventions, inherits the operator's gh credentials, and runs over a single owner/repo per call.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner":  {Type: "string", Description: "repository owner (user or org)"},
				"repo":   {Type: "string", Description: "repository name"},
				"author": {Type: "string", Description: "the GitHub login whose state-mutation maintenance activity is measured (matched by login; an unknown login yields zero items)"},
				"since":  {Type: "string", Description: "window start as an RFC3339 timestamp (e.g. 2026-01-01T00:00:00Z)"},
				"until":  {Type: "string", Description: "window end as an RFC3339 timestamp; defaults to now when omitted"},
			},
			Required: []string{"owner", "repo", "author", "since"},
		},
	}
}

// maintenanceActivityHandler validates the window and actor, fetches the
// repository's issue-events stream back to the window floor, and reduces it to the
// grouped per-item facts. It reads no manifest (the primitive is actor/window-
// driven). The window is parsed and ordered before any fetch, so a malformed or
// inverted window fails fast; a throttle surfaces its retry instant like the other
// tools. An unknown actor is not an error — it simply yields zero items.
func maintenanceActivityHandler(fetcher github.Fetcher, now func() time.Time) mcp.ToolHandlerFor[maintenanceActivityInput, maintenance.Facts] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in maintenanceActivityInput) (*mcp.CallToolResult, maintenance.Facts, error) {
		owner, repo := strings.TrimSpace(in.Owner), strings.TrimSpace(in.Repo)
		author := strings.TrimSpace(in.Author)
		if owner == "" || repo == "" {
			return nil, maintenance.Facts{}, fmt.Errorf("owner and repo are required")
		}
		if author == "" {
			return nil, maintenance.Facts{}, fmt.Errorf("author is required")
		}
		ownerRepo := owner + "/" + repo

		n := now()
		since, until, werr := parseWindow(in.Since, in.Until, n)
		if werr != nil {
			return nil, maintenance.Facts{}, werr
		}

		result, err := fetcher.ListIssueEvents(ctx, ownerRepo, since, maintenanceFetchLimit)
		if err != nil {
			if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
				if when := rateLimitResetTime(rle, now); !when.IsZero() {
					return nil, maintenance.Facts{}, fmt.Errorf("fetching maintenance activity for %s: %w (retry after %s)", ownerRepo, err, when.UTC().Format(time.RFC3339))
				}
			}
			return nil, maintenance.Facts{}, fmt.Errorf("fetching maintenance activity for %s: %w", ownerRepo, err)
		}

		facts := maintenance.Reduce(result, author, since, until)
		facts.Repo = ownerRepo
		facts.GeneratedAt = n
		facts.RateLimit = mapRateLimit(result.RateLimit)
		return nil, facts, nil
	}
}

const (
	// maintenanceBatchConcurrency bounds how many repos fetch at once. Each repo is a
	// paginated REST scan against the core pool (requests/hour), which is roomier and
	// less burst-sensitive than the search secondary limit the authored batch paces
	// against — but kept modest so a wide batch does not spike concurrent REST load.
	maintenanceBatchConcurrency = 4
	// maintenanceBatchMaxRepos caps a single batch; it mirrors the schema's maxItems.
	maintenanceBatchMaxRepos = 50
	// maintenanceBatchPerRepoTimeout bounds one repo's whole paginated fetch, so a
	// single hung or pathological repo degrades to its own fetch_failed marker without
	// holding a concurrency slot for the full transport timeout. A healthy repo returns
	// far inside this budget.
	maintenanceBatchPerRepoTimeout = 45 * time.Second
)

// maintenanceActivityBatchInput is the batch tool's decoded input: a list of
// owner/repo measured for one actor over one window. Until is optional (defaults
// to now); constraints live in the published schema.
type maintenanceActivityBatchInput struct {
	Repos  []string `json:"repos"`
	Author string   `json:"author"`
	Since  string   `json:"since"`
	Until  string   `json:"until"`
}

// maintenanceActivityBatchTool publishes the batch input contract. repos is a
// non-empty, bounded array of owner/repo; author/since are required; until is
// optional.
func maintenanceActivityBatchTool() *mcp.Tool {
	minRepos, maxRepos := 1, maintenanceBatchMaxRepos
	return &mcp.Tool{
		Name:        "maintenance_activity_batch",
		Description: "Measure the state-mutation maintenance one GitHub user paid to existing issues and pull requests across several repositories over a caller-supplied time window — the batched form of maintenance_activity. Given a list of owner/repo and one actor login, it fans out per repository and returns one entry per repository in request order, each either the touched items (most-recently-touched first, the actor's qualifying mutations grouped under each, with isPullRequest per item and viaAutomation per event) or one of four per-repo markers: not_found, rate_limited, fetch_failed, not_attempted. A not_found or fetch_failed repository degrades to its own marker without sinking the others; a rate_limited repository additionally trips backpressure (the batch stops launching new fetches to avoid amplifying the limit, so an arbitrary subset of the not-yet-started repositories returns not_attempted — a deliberate skip, not a failure). The batch surfaces a single aggregated rate-limit budget: the tightest remaining across the successful repositories, or a throttle's reset instant when any repository was throttled. That budget is the REST core pool (requests per hour) — a different pool from authored_activity_batch's GraphQL points, so a caller must not compare or combine the two batches' budgets. Unlike the authored batch there is no whole-batch author error: the actor is matched by login string, so an unknown or inactive login simply yields zero items per repository. The tool reads no manifest conventions, inherits the operator's gh credentials, and returns per-repo facts only — merging across repositories and the attention verdict stay caller-side.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"repos": {
					Type:        "array",
					Description: "the repositories to measure, each as an owner/repo slug",
					Items:       &jsonschema.Schema{Type: "string"},
					MinItems:    &minRepos,
					MaxItems:    &maxRepos,
				},
				"author": {Type: "string", Description: "the GitHub login whose state-mutation maintenance activity is measured (matched by login; an unknown login yields zero items)"},
				"since":  {Type: "string", Description: "window start as an RFC3339 timestamp (e.g. 2026-01-01T00:00:00Z)"},
				"until":  {Type: "string", Description: "window end as an RFC3339 timestamp; defaults to now when omitted"},
			},
			Required: []string{"repos", "author", "since"},
		},
	}
}

// maintenanceActivityBatchHandler validates the actor, the repo list, and the
// window, fans out the per-repo fetches, and reduces them to batch facts. It reads
// no manifest. Unlike the authored batch there is no whole-batch author escalation:
// the actor is a stream filter, not a resolved identity, so an unknown actor yields
// zero items per repo rather than a repo-independent error.
func maintenanceActivityBatchHandler(fetcher github.Fetcher, now func() time.Time, concurrency int, perRepoTimeout time.Duration) mcp.ToolHandlerFor[maintenanceActivityBatchInput, maintenance.BatchFacts] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in maintenanceActivityBatchInput) (*mcp.CallToolResult, maintenance.BatchFacts, error) {
		author := strings.TrimSpace(in.Author)
		if author == "" {
			return nil, maintenance.BatchFacts{}, fmt.Errorf("author is required")
		}
		repos, verr := validateRepos(in.Repos, maintenanceBatchMaxRepos)
		if verr != nil {
			return nil, maintenance.BatchFacts{}, verr
		}
		n := now()
		since, until, werr := parseWindow(in.Since, in.Until, n)
		if werr != nil {
			return nil, maintenance.BatchFacts{}, werr
		}

		entries := fanOutMaintenance(ctx, fetcher, repos, since, now, concurrency, perRepoTimeout)
		// A cancelled request must surface as an error, not a fabricated success built
		// from the placeholder markers the fan-out stamps for not-yet-started repos.
		if cerr := ctx.Err(); cerr != nil {
			return nil, maintenance.BatchFacts{}, fmt.Errorf("maintenance_activity_batch cancelled: %w", cerr)
		}

		facts := maintenance.ReduceBatch(entries, author, since, until)
		facts.GeneratedAt = n
		return nil, facts, nil
	}
}

// fanOutMaintenance fetches each repo's issue events concurrently (bounded by
// concurrency) and classifies every outcome into a BatchEntry, so one repo's
// failure degrades only its own entry. It is a deliberate parallel of
// fanOutAuthored rather than a shared generic: the two differ in classification
// (authored threads an author-not-found sentinel that maintenance has no analog
// for) and budget pool (GraphQL points vs REST requests), and a premature generic
// would have to parameterize the sentinel-to-marker mapping while risking the
// landed authored race tests' timing assumptions. The actor is not passed here —
// it is a reduction-side filter, not a fetch parameter; only `since` reaches the
// fetch (the floor it scans back to).
//
// The same two adverse-condition adaptations as the authored fan-out layer on top:
// a throttle on any repo trips stopLaunch so not-yet-started repos are skipped as
// not_attempted rather than amplifying the rate limit, and each fetch carries a
// perRepoTimeout deadline so a hung repo degrades to fetch_failed without stalling
// the rest.
func fanOutMaintenance(ctx context.Context, fetcher github.Fetcher, repos []string, since time.Time, now func() time.Time, concurrency int, perRepoTimeout time.Duration) []maintenance.BatchEntry {
	entries := make([]maintenance.BatchEntry, len(repos))
	// Guard the tuning parameters so a misconfigured internal value degrades safely
	// rather than hanging (a zero buffer makes the semaphore block forever) or
	// failing every repo (a non-positive timeout fires an immediate deadline).
	if concurrency < 1 {
		concurrency = 1
	}
	if perRepoTimeout <= 0 {
		perRepoTimeout = maintenanceBatchPerRepoTimeout
	}
	sem := make(chan struct{}, concurrency)
	// stopLaunch is the throttle backpressure flag (atomic, not ctx cancellation, so
	// in-flight fetches on the parent ctx still complete rather than aborting into
	// fetch_failed). Once any repo throttles, new launches are skipped.
	var stopLaunch atomic.Bool
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				entries[i] = maintenance.BatchEntry{Repo: repo, Unavailable: maintenance.UnavailableFetchFailed}
				return
			}
			if stopLaunch.Load() {
				entries[i] = maintenance.BatchEntry{Repo: repo, Unavailable: maintenance.UnavailableNotAttempted}
				return
			}
			entry := fetchMaintenanceEntry(ctx, fetcher, repo, since, now, perRepoTimeout)
			if entry.Unavailable == maintenance.UnavailableRateLimited {
				stopLaunch.Store(true)
			}
			entries[i] = entry
		})
	}
	wg.Wait()
	return entries
}

// fetchMaintenanceEntry fetches one repo's events under a perRepoTimeout deadline
// (derived from ctx, so a hung repo can't hold its slot for the full transport
// timeout) and maps the outcome to a BatchEntry: the raw events on success (the
// actor/window filter is applied later by ReduceBatch), else a per-repo marker. A
// throttle carries its resolved reset instant; any other failure — including a
// deadline trip, which matches no sentinel — is fetch_failed, its cause logged to
// stderr. There is no author-not-found path: the events fetch resolves no actor.
func fetchMaintenanceEntry(ctx context.Context, fetcher github.Fetcher, repo string, since time.Time, now func() time.Time, perRepoTimeout time.Duration) maintenance.BatchEntry {
	if ctx.Err() != nil {
		return maintenance.BatchEntry{Repo: repo, Unavailable: maintenance.UnavailableFetchFailed}
	}
	ctx, cancel := context.WithTimeout(ctx, perRepoTimeout)
	defer cancel()
	result, err := fetcher.ListIssueEvents(ctx, repo, since, maintenanceFetchLimit)
	if err == nil {
		return maintenance.BatchEntry{Repo: repo, Result: result}
	}
	if errors.Is(err, github.ErrRepoNotFound) {
		return maintenance.BatchEntry{Repo: repo, Unavailable: maintenance.UnavailableNotFound}
	}
	if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
		return maintenance.BatchEntry{Repo: repo, Unavailable: maintenance.UnavailableRateLimited, ResetAt: rateLimitResetTime(rle, now)}
	}
	log.Printf("overstory: maintenance activity fetch for %s: %v", repo, err)
	return maintenance.BatchEntry{Repo: repo, Unavailable: maintenance.UnavailableFetchFailed}
}

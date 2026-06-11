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
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jakewan/overstory/internal/backlog"
	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/manifest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "overstory"
	serverVersion = "0.1.0"
)

// config holds the server's resolved dependencies. Options override the
// production defaults; tests inject fakes for hermetic coverage.
type config struct {
	fetcher       github.IssueFetcher
	manifestRoot  string
	manifestFiles []string
	now           func() time.Time
}

// Option configures the server's dependencies.
type Option func(*config)

// WithFetcher overrides the GitHub issue fetcher (tests inject a fake).
func WithFetcher(f github.IssueFetcher) Option {
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

// New builds the overstory MCP server and registers the backlog_review tool.
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

	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	mcp.AddTool(srv, backlogReviewTool(), backlogReviewHandler(resolver, cfg.fetcher, cfg.now))
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
	for _, p := range strings.Split(raw, ":") {
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
		Description: "Survey a GitHub repository's open-issue backlog and return compact structured facts for the caller to render: a staleness block (exact open count, inactivity-band counts, the stalest issues), a deferred-review block (open issues carrying the repo's manifest-declared deferred labels), an area-balance block (the issue distribution across the repo's functional areas, identified by manifest-declared labels and prefixes), a quality block (open issues with a too-thin body, no labels, or — when configured — a missing required-label category), an overlap block (groups of open issues with similar titles — candidate duplicates — found over the fetched window), a cross-reference block (groups of open issues that reference one another issue-to-issue via GitHub cross-references — candidate consolidation — found over the fetched window), and a trajectory block (for each manifest-declared lookback window in days, the issues created, closed, and net created-minus-closed — the backlog growing/shrinking signal — over a second open-and-closed fetch; this block is aggregate and not affected by limit, and marks itself unavailable if that fetch fails rather than failing the whole review).",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner": {Type: "string", Description: "repository owner (user or org)"},
				"repo":  {Type: "string", Description: "repository name"},
				"limit": {
					Type:        "integer",
					Description: "maximum number of items to list per reduction: issues for staleness, deferred, and quality; overlap groups for overlap; cross-reference groups for crossRef",
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
// trajectory). Most blocks reduce the one open-issue fetch; trajectory adds a
// second open-and-closed fetch and degrades to an unavailable block on failure.
// Errors from the open fetch are returned plain so the SDK surfaces them as tool
// errors (IsError); a manifest error names a file, so it is logged to stderr and
// replaced with a repo-named message on the caller channel.
func backlogReviewHandler(resolver *manifest.Resolver, fetcher github.IssueFetcher, now func() time.Time) mcp.ToolHandlerFor[backlogReviewInput, backlog.Facts] {
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
			var rle github.RateLimitedError
			if errors.As(err, &rle) {
				if when := rateLimitResetTime(rle, now); !when.IsZero() {
					return nil, backlog.Facts{}, fmt.Errorf("fetching issues for %s: %w (retry after %s)", ownerRepo, err, when.UTC().Format(time.RFC3339))
				}
			}
			return nil, backlog.Facts{}, fmt.Errorf("fetching issues for %s: %w", ownerRepo, err)
		}

		// Bind the clock once so every block shares one generation time; the two
		// reductions run over the same fetched window.
		n := now()
		staleness := backlog.ReduceStaleness(result.Issues, result.TotalOpen, cfg.Staleness.ThresholdDays, in.Limit, n)
		staleness.FetchLimit = cfg.Staleness.FetchLimit
		staleness.ThresholdSource = thresholdSource(matched)
		deferred := backlog.ReduceDeferred(result.Issues, result.TotalOpen, cfg.Deferred.Labels, in.Limit, n)
		area := backlog.ReduceAreaBalance(result.Issues, result.TotalOpen, cfg.AreaBalance.Labels, mapPrefixes(cfg.AreaBalance.Prefixes))
		quality := backlog.ReduceQuality(result.Issues, result.TotalOpen, mapQuality(cfg.Quality), in.Limit, n)
		overlap := backlog.ReduceOverlap(result.Issues, result.TotalOpen, backlog.OverlapParams{TitleThreshold: cfg.Overlap.TitleSimilarityThreshold}, in.Limit)
		crossref := backlog.ReduceCrossRef(result.Issues, result.TotalOpen, in.Limit)

		// Trajectory needs a second fetch (closed issues too); a failure there
		// degrades the block rather than failing the whole review, since the other
		// five blocks already reduced the successful open-issue fetch.
		trajectory, budget := reduceTrajectory(ctx, fetcher, ownerRepo, cfg.Trajectory, result.RateLimit, n, now)

		return nil, backlog.Facts{
			Repo:        ownerRepo,
			GeneratedAt: n,
			Staleness:   staleness,
			Deferred:    deferred,
			AreaBalance: area,
			Quality:     quality,
			Overlap:     overlap,
			CrossRef:    crossref,
			Trajectory:  trajectory,
			RateLimit:   mapRateLimit(budget),
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
func reduceTrajectory(ctx context.Context, fetcher github.IssueFetcher, ownerRepo string, cfg manifest.TrajectoryConfig, openBudget *github.RateLimit, n time.Time, now func() time.Time) (backlog.TrajectoryFacts, *github.RateLimit) {
	since := n.UTC().AddDate(0, 0, -maxInt(cfg.Windows))
	activity, err := fetcher.ListIssuesUpdatedSince(ctx, ownerRepo, since, cfg.FetchLimit)
	if err == nil {
		return backlog.ReduceTrajectory(activity.Activities, cfg.Windows, activity.Truncated, n), freshestBudget(openBudget, activity.RateLimit)
	}
	var rle github.RateLimitedError
	if errors.As(err, &rle) {
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

// mapRateLimit adapts the fetch's budget snapshot to the backlog fact, keeping
// the reduction layer decoupled from the github layer; nil (no budget observed)
// passes through so the fact is omitted from the output.
func mapRateLimit(in *github.RateLimit) *backlog.RateLimitFacts {
	if in == nil {
		return nil
	}
	return &backlog.RateLimitFacts{Remaining: in.Remaining, ResetAt: in.ResetAt}
}

func thresholdSource(matched bool) string {
	if matched {
		return "manifest"
	}
	return "default"
}

// mapPrefixes adapts the manifest's prefix rules to the backlog matcher's, so the
// reduction layer stays decoupled from the convention-resolution layer.
func mapPrefixes(in []manifest.PrefixRule) []backlog.PrefixRule {
	out := make([]backlog.PrefixRule, len(in))
	for i, p := range in {
		out[i] = backlog.PrefixRule{Prefix: p.Prefix, Delimiter: p.Delimiter}
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

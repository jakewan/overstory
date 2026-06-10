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
		Description: "Survey a GitHub repository's open-issue backlog and return compact structured facts for the caller to render: a staleness block (exact open count, inactivity-band counts, the stalest issues), a deferred-review block (open issues carrying the repo's manifest-declared deferred labels), an area-balance block (the issue distribution across the repo's functional areas, identified by manifest-declared labels and prefixes), and a quality block (open issues with a too-thin body, no labels, or — when configured — a missing required-label category).",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner": {Type: "string", Description: "repository owner (user or org)"},
				"repo":  {Type: "string", Description: "repository name"},
				"limit": {
					Type:        "integer",
					Description: "maximum number of issues to list per reduction (staleness, deferred, quality)",
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
// and reduces them to the composite backlog facts (a staleness block and a
// deferred-issue block). Errors are returned plain so the SDK surfaces them as
// tool errors (IsError); a manifest error names a file, so it is logged to
// stderr and replaced with a repo-named message on the caller channel.
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
		return nil, backlog.Facts{
			Repo:        ownerRepo,
			GeneratedAt: n,
			Staleness:   staleness,
			Deferred:    deferred,
			AreaBalance: area,
			Quality:     quality,
		}, nil
	}
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

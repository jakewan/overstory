package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/manifest"
	"github.com/jakewan/overstory/internal/summary"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// projectSummaryInput is the tool's decoded input. Constraints (required fields,
// limit default and bounds) live in the published schema, not here.
type projectSummaryInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Limit int    `json:"limit"`
}

// projectSummaryTool publishes the input contract via a hand-written schema, the
// same way backlogReviewTool does (the installed jsonschema-go infers neither
// defaults nor bounds from struct tags): owner/repo required, limit optional with
// a default and 1..100 bounds the SDK applies before the handler runs.
func projectSummaryTool() *mcp.Tool {
	minLimit, maxLimit := 1.0, 100.0
	return &mcp.Tool{
		Name:        "project_summary",
		Description: "Survey a GitHub repository for session orientation — \"given what's open now, what should I pick up?\" — and return compact structured facts for the caller to render: a milestones block (each open milestone's authoritative open/closed counts plus the fetched open issues belonging to it, with a per-milestone flag when that member list is a floor relative to the open count), an area-inventory block (per functional area, the active-vs-deferred split of its open issues, areas identified by the repo's manifest labels and prefixes), a hygiene block (four signals over the open issues: missing-area, unmilestoned-and-aged, stale, and deferred-without-context), an open-PRs block (each open pull request's branch, draft/ready state, CI rollup, and inactivity, plus a stale-PR count), and a recommendations block (per-issue inputs — bug-labeled, milestone, age, inactivity — a caller ranks 'what next' from; the ranking judgment stays caller-side). The milestones and open-PRs blocks each need their own fetch and mark themselves unavailable (with a rate_limited/fetch_failed reason) if that fetch fails, rather than failing the whole summary.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner": {Type: "string", Description: "repository owner (user or org)"},
				"repo":  {Type: "string", Description: "repository name"},
				// The minimum and default are load-bearing, not just ergonomics: each
				// reduction treats a listLimit of 0 as "empty every list", so this bound
				// is what keeps in.Limit — and every reduction's cap — at 1 or more.
				"limit": {
					Type:        "integer",
					Description: "maximum number of items to list per reduction: members per milestone and the milestone list, issues per hygiene signal, open PRs, and recommendation candidates",
					Default:     json.RawMessage("20"),
					Minimum:     &minLimit,
					Maximum:     &maxLimit,
				},
			},
			Required: []string{"owner", "repo"},
		},
	}
}

// projectSummaryHandler resolves the repo's conventions, fetches its open issues,
// milestones, and pull requests, and reduces them to the composite orientation
// facts. The open-issue fetch is primary: its failure fails the whole call (a
// throttle names its retry instant). The milestone and PR fetches are secondary:
// each degrades its own block to unavailable on failure rather than failing the
// summary, so the issue-derived blocks stay useful. A manifest error names a
// file, so it is logged to stderr and replaced with a repo-named message.
func projectSummaryHandler(resolver *manifest.Resolver, fetcher github.Fetcher, now func() time.Time) mcp.ToolHandlerFor[projectSummaryInput, summary.Facts] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in projectSummaryInput) (*mcp.CallToolResult, summary.Facts, error) {
		owner, repo := strings.TrimSpace(in.Owner), strings.TrimSpace(in.Repo)
		if owner == "" || repo == "" {
			return nil, summary.Facts{}, fmt.Errorf("owner and repo are required")
		}
		ownerRepo := owner + "/" + repo

		cfg, _, err := resolver.Resolve(ownerRepo)
		if err != nil {
			log.Printf("overstory: project_summary: manifest resolution for %s: %v", ownerRepo, err)
			return nil, summary.Facts{}, fmt.Errorf("manifest configuration error for %s", ownerRepo)
		}

		// Primary fetch: the open-issue window most blocks reduce. Reuse the staleness
		// window cap — it is the repo's "how many open issues to fetch" knob.
		result, err := fetcher.ListOpenIssues(ctx, ownerRepo, cfg.Staleness.FetchLimit)
		if err != nil {
			var rle github.RateLimitedError
			if errors.As(err, &rle) {
				if when := rateLimitResetTime(rle, now); !when.IsZero() {
					return nil, summary.Facts{}, fmt.Errorf("fetching issues for %s: %w (retry after %s)", ownerRepo, err, when.UTC().Format(time.RFC3339))
				}
			}
			return nil, summary.Facts{}, fmt.Errorf("fetching issues for %s: %w", ownerRepo, err)
		}

		// One generation time shared by every block.
		n := now()
		issues, totalOpen := result.Issues, result.TotalOpen

		milestones, msBudget := summaryMilestones(ctx, fetcher, ownerRepo, cfg.Summary, issues, in.Limit, n, now)
		prs, prBudget := summaryPullRequests(ctx, fetcher, ownerRepo, cfg.Summary, in.Limit, n, now)

		area := summary.ReduceAreaInventory(issues, totalOpen, cfg.AreaBalance.Labels, mapPrefixes(cfg.AreaBalance.Prefixes), cfg.Deferred.Labels)
		hygiene := summary.ReduceHygiene(issues, totalOpen, summary.HygieneParams{
			AreaLabels:          cfg.AreaBalance.Labels,
			AreaPrefixes:        mapPrefixes(cfg.AreaBalance.Prefixes),
			DeferredLabels:      cfg.Deferred.Labels,
			UnmilestonedAgeDays: cfg.Summary.UnmilestonedAgeDays,
			StaleThresholdDays:  cfg.Staleness.ThresholdDays,
			ContextBodyLength:   cfg.Quality.MinBodyLength,
		}, in.Limit, n)
		recommendations := summary.ReduceRecommendations(issues, totalOpen, cfg.Summary.BugLabels, in.Limit, n)

		return nil, summary.Facts{
			Repo:            ownerRepo,
			GeneratedAt:     n,
			Milestones:      milestones,
			AreaInventory:   area,
			Hygiene:         hygiene,
			OpenPRs:         prs,
			Recommendations: recommendations,
			// The tightest budget across the three fetches: a caller pacing itself must
			// see the lowest remaining ceiling, and a throttle's zero-remaining signal
			// (from a degraded sub-fetch) wins so the caller learns it is throttled.
			RateLimit: mapRateLimit(tightestBudget(result.RateLimit, msBudget, prBudget)),
		}, nil
	}
}

// summaryMilestones runs the milestone fetch and reduces it, degrading to an
// unavailable block on failure (mirroring reduceTrajectory): a rate-limit names
// its reason and returns a zero-remaining budget carrying the reset instant so a
// throttled caller learns why and when; any other failure stays on stderr and
// returns a nil budget.
func summaryMilestones(ctx context.Context, fetcher github.Fetcher, ownerRepo string, cfg manifest.SummaryConfig, issues []github.Issue, limit int, n time.Time, now func() time.Time) (summary.MilestoneFacts, *github.RateLimit) {
	res, err := fetcher.ListOpenMilestones(ctx, ownerRepo, cfg.MilestoneFetchLimit)
	if err == nil {
		truncated := len(res.Milestones) < res.TotalOpen
		return summary.ReduceMilestones(res.Milestones, res.TotalOpen, truncated, issues, limit, n), res.RateLimit
	}
	var rle github.RateLimitedError
	if errors.As(err, &rle) {
		return summary.MilestoneFacts{Available: false, Unavailable: "rate_limited", Milestones: []summary.MilestoneProgress{}},
			&github.RateLimit{Remaining: 0, ResetAt: rateLimitResetTime(rle, now)}
	}
	log.Printf("overstory: milestone fetch for %s: %v", ownerRepo, err)
	return summary.MilestoneFacts{Available: false, Unavailable: "fetch_failed", Milestones: []summary.MilestoneProgress{}}, nil
}

// summaryPullRequests runs the pull-request fetch and reduces it, degrading the
// same way as summaryMilestones on failure.
func summaryPullRequests(ctx context.Context, fetcher github.Fetcher, ownerRepo string, cfg manifest.SummaryConfig, limit int, n time.Time, now func() time.Time) (summary.PullRequestFacts, *github.RateLimit) {
	res, err := fetcher.ListOpenPullRequests(ctx, ownerRepo, cfg.PRFetchLimit)
	if err == nil {
		truncated := len(res.PullRequests) < res.TotalOpen
		return summary.ReducePullRequests(res.PullRequests, res.TotalOpen, truncated, cfg.PRStalenessDays, limit, n), res.RateLimit
	}
	var rle github.RateLimitedError
	if errors.As(err, &rle) {
		return summary.PullRequestFacts{Available: false, Unavailable: "rate_limited", PullRequests: []summary.PullRequestState{}},
			&github.RateLimit{Remaining: 0, ResetAt: rateLimitResetTime(rle, now)}
	}
	log.Printf("overstory: pull-request fetch for %s: %v", ownerRepo, err)
	return summary.PullRequestFacts{Available: false, Unavailable: "fetch_failed", PullRequests: []summary.PullRequestState{}}, nil
}

// tightestBudget returns the budget with the fewest points remaining across the
// fetches (nils ignored), or nil when none carried a budget. The minimum is what a
// caller must pace against — and a degraded sub-fetch's zero-remaining throttle
// signal wins, so the caller is never told it has budget at the moment it was
// throttled.
func tightestBudget(budgets ...*github.RateLimit) *github.RateLimit {
	var tightest *github.RateLimit
	for _, b := range budgets {
		if b == nil {
			continue
		}
		if tightest == nil || b.Remaining < tightest.Remaining {
			tightest = b
		}
	}
	return tightest
}

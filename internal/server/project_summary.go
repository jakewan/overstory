package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jakewan/overstory/internal/dependency"
	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/manifest"
	"github.com/jakewan/overstory/internal/reduce"
	"github.com/jakewan/overstory/internal/summary"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// projectSummaryInput is the tool's decoded input. Constraints (required fields,
// limit default and bounds) live in the published schema, not here.
type projectSummaryInput struct {
	Owner  string   `json:"owner"`
	Repo   string   `json:"repo"`
	Limit  int      `json:"limit"`
	Blocks []string `json:"blocks"`
}

// summaryBlockNames are the projectable project_summary blocks, in schema/enum
// order — the single source of truth for the blocks parameter's enum and the
// handler's projection set. Excludes the always-present meta blocks (repo,
// generatedAt, openIssueSet, rateLimit, sizeBound).
var summaryBlockNames = []string{
	"milestones", "areaInventory", "hygiene", "openPRs", "recommendations", "criticalPath", "dependencies",
}

// projectSummaryTool publishes the input contract via a hand-written schema, the
// same way backlogReviewTool does (the installed jsonschema-go infers neither
// defaults nor bounds from struct tags): owner/repo required, limit optional with
// a default and the shared list-limit bounds the SDK applies before the handler runs.
func projectSummaryTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "project_summary",
		Description: "Orientation read of a GitHub repository, what to pick up next, as facts for the caller to render. Blocks: milestones (each open milestone's exact open/closed counts and fetched members, flagged when members are a floor); areaInventory (per area, the active-vs-deferred split); hygiene (signal counts; when areaLabelsObserved is false no configured area label was seen, so treat missing-area as an observation to investigate, not defects to close; labelTruncatedCount issues had labels capped, so missing-area and stale may read high, deferred-without-context low); openPRs (each open pull request's state, CI rollup, and inactivity); recommendations (per-issue ranking inputs, including heuristic bodyRefs, native blockedBy/blockedByExternal/blocking/sub-issue edges, and a readiness verdict); criticalPath (per declared stream, its open critical-path issues and whether its gate is cleared, provisional under a truncated fetch or labelTruncatedCount); dependencies (convention-free ready/blocked/provisional counts and gates with their blockingCount; per-issue edges live in recommendations). Always returned: repo, generatedAt, and openIssueSet (the open issue numbers bodyRefs resolve against). Absence from openIssueSet is not proof of resolution: the ref may be closed, a pull request, cross-repository, or past a truncated window. Readiness is decided server-side, the same verdict the dependencies block counts; ranking stays with the caller. Blocked means an open blocked-by edge in any repository or an open sub-issue gate; provisional means no blocker was seen but a truncated edge list or an unread seam leaves it unconfirmed, since an empty edge list is not evidence of readiness. Issue-derived blocks cover the fetched window, fetchTruncated marking a floor, though every openIssueCount stays exact; listTruncated means a list was cut at limit. Blocks with their own fetch carry their own flags and degrade to available:false. sizeBound marks a size-trimmed response.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner": {Type: "string", Description: "repository owner (user or org)"},
				"repo":  {Type: "string", Description: "repository name"},
				"limit": limitSchema("maximum number of items to list per reduction: members per milestone and the milestone list, issues per hygiene signal, open PRs, recommendation candidates, and members per stream for criticalPath"),
				"blocks": {
					Type:        "array",
					Description: "optional allowlist of block names to return; omit it (or pass an empty array) for the full composite. Projecting a subset omits the other blocks from the response and skips the secondary GitHub fetch backing any block not requested (milestones, openPRs), saving rate-limit budget when fanning out across many repos. The meta blocks (repo, generatedAt, openIssueSet) are always returned.",
					Items:       &jsonschema.Schema{Type: "string", Enum: blockEnum(summaryBlockNames)},
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

		// Resolve the projection request before any fetch, so an invalid block name
		// fails fast. An empty request means full composite.
		want, err := requestedBlocks(in.Blocks, summaryBlockNames, "project_summary")
		if err != nil {
			return nil, summary.Facts{}, err
		}

		cfg, _, err := resolver.Resolve(ownerRepo)
		if err != nil {
			log.Printf("overstory: project_summary: manifest resolution for %s: %v", ownerRepo, err)
			return nil, summary.Facts{}, fmt.Errorf("manifest configuration error for %s", ownerRepo)
		}

		// Primary fetch: the open-issue window most blocks reduce. Reuse the staleness
		// window cap — it is the repo's "how many open issues to fetch" knob.
		result, err := fetcher.ListOpenIssues(ctx, ownerRepo, cfg.Staleness.FetchLimit)
		if err != nil {
			if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
				if when := rateLimitResetTime(rle, now); !when.IsZero() {
					return nil, summary.Facts{}, fmt.Errorf("fetching issues for %s: %w (retry after %s)", ownerRepo, err, when.UTC().Format(time.RFC3339))
				}
			}
			return nil, summary.Facts{}, fmt.Errorf("fetching issues for %s: %w", ownerRepo, err)
		}

		// One generation time shared by every block.
		n := now()
		// Reconcile the seam states against what this forge carries once, before any
		// reduction reads them, so every block of the response agrees about the same
		// issue rather than each reduction deciding for itself.
		issues, totalOpen := github.ApplyCapabilities(result.Issues, result.Capabilities), result.TotalOpen

		// Primary (non-fetch) reductions run unconditionally over the fetched window;
		// projection gates serialization (below) and the secondary fetches, never these
		// cheap pure reductions (see backlogReviewHandler for the coupling rationale).
		area := summary.ReduceAreaInventory(issues, totalOpen, cfg.AreaBalance.Labels, mapPrefixes(cfg.AreaBalance.Prefixes), cfg.Deferred.Labels)
		hygiene := summary.ReduceHygiene(issues, totalOpen, summary.HygieneParams{
			AreaLabels:          cfg.AreaBalance.Labels,
			AreaPrefixes:        mapPrefixes(cfg.AreaBalance.Prefixes),
			DeferredLabels:      cfg.Deferred.Labels,
			UnmilestonedAgeDays: cfg.Summary.UnmilestonedAgeDays,
			StaleThresholdDays:  cfg.Staleness.ThresholdDays,
			ContextBodyLength:   cfg.Quality.MinBodyLength,
		}, in.Limit, n)
		recommendations := summary.ReduceRecommendations(issues, totalOpen, cfg.Summary.BugLabels, in.Limit, result.Capabilities, n)
		// The classification-only projection: recommendations already ships every
		// candidate's raw blocked-by/blocking edges, so the summary adds the graph-level
		// ready/blocked split and the gate set, not a second copy of the edges.
		dependencies := dependency.Reduce(issues, totalOpen, in.Limit, result.Capabilities).Classification()

		facts := summary.Facts{
			Repo:        ownerRepo,
			GeneratedAt: n,
			// The full fetched open-issue window, never capped by in.Limit: a caller
			// resolves a candidate's bodyRefs against this set, so a real open blocker
			// beyond the list cap must still appear here or the contract would lie.
			// Always present.
			OpenIssueSet: reduce.NewOpenIssueSet(openIssueNumbers(issues), len(issues) < totalOpen),
		}
		if want["areaInventory"] {
			facts.AreaInventory = &area
		}
		if want["hygiene"] {
			facts.Hygiene = &hygiene
		}
		if want["recommendations"] {
			facts.Recommendations = &recommendations
		}
		if want["dependencies"] {
			facts.Dependencies = &dependencies
		}

		// Milestones and PRs each need their own fetch and run only when requested.
		// Each degrades to a non-nil unavailable block on its own failure. The tightest
		// budget across the fetches that ran: a caller pacing itself must see the lowest
		// remaining ceiling, and a degraded sub-fetch's zero-remaining throttle wins.
		budgets := []*github.RateLimit{result.RateLimit}
		if want["criticalPath"] {
			cp, cpBudget := criticalPathBlock(ctx, fetcher, ownerRepo, cfg, issues, totalOpen, in.Limit, now)
			facts.CriticalPath = &cp
			budgets = append(budgets, cpBudget)
		}
		if want["milestones"] {
			milestones, msBudget := summaryMilestones(ctx, fetcher, ownerRepo, cfg.Summary, issues, in.Limit, n, now)
			facts.Milestones = &milestones
			budgets = append(budgets, msBudget)
		}
		if want["openPRs"] {
			prs, prBudget := summaryPullRequests(ctx, fetcher, ownerRepo, cfg.Summary, in.Limit, n, now)
			facts.OpenPRs = &prs
			budgets = append(budgets, prBudget)
		}
		facts.RateLimit = mapRateLimit(tightestBudget(budgets...))

		// Bound the total response (see backlogReviewHandler). Trim the flat detail
		// lists of the present blocks only; criticalPath members and counts/openIssueSet
		// are preserved.
		var units []reduce.Trimmable
		if facts.Hygiene != nil {
			units = append(units,
				trimUnit("hygiene.missingArea", &facts.Hygiene.MissingArea.Issues, &facts.Hygiene.MissingArea.ListTruncated),
				trimUnit("hygiene.unmilestonedAged", &facts.Hygiene.UnmilestonedAged.Issues, &facts.Hygiene.UnmilestonedAged.ListTruncated),
				trimUnit("hygiene.stale", &facts.Hygiene.Stale.Issues, &facts.Hygiene.Stale.ListTruncated),
				trimUnit("hygiene.deferredWithoutContext", &facts.Hygiene.DeferredWithoutContext.Issues, &facts.Hygiene.DeferredWithoutContext.ListTruncated),
			)
		}
		if facts.OpenPRs != nil {
			units = append(units, trimUnit("openPRs", &facts.OpenPRs.PullRequests, &facts.OpenPRs.ListTruncated))
		}
		if facts.Recommendations != nil {
			units = append(units, trimUnit("recommendations", &facts.Recommendations.Candidates, &facts.Recommendations.ListTruncated))
		}
		if facts.Dependencies != nil {
			units = append(units, trimUnit("dependencies:gates", &facts.Dependencies.Gates, &facts.Dependencies.GatesTruncated))
		}
		// Trim milestone members, not whole milestones. Each milestone's progress entry
		// (title, open/closed counts) is the headline orientation signal; the nested
		// member lists carry the bytes. Per-milestone units keep every entry and shed
		// only detail, surfaced via the existing membershipTruncated. Dropping whole
		// milestones would shed the newest first (progress is sorted by number
		// ascending), gutting the active milestone exactly when the bound fires.
		if facts.Milestones != nil {
			for i := range facts.Milestones.Milestones {
				m := &facts.Milestones.Milestones[i]
				units = append(units, trimUnit(fmt.Sprintf("milestones[#%d].members", m.Number), &m.Members, &m.MembershipTruncated))
			}
		}
		if err := boundResponse(&facts, &facts.SizeBound, cfg.Response.MaxBytes, units); err != nil {
			return nil, summary.Facts{}, fmt.Errorf("bounding response for %s: %w", ownerRepo, err)
		}
		return nil, facts, nil
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
	if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
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
	if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
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

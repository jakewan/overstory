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

// milestoneTracksInput is the tool's decoded input. Constraints (required fields,
// limit default and bounds) live in the published schema, not here.
type milestoneTracksInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Limit int    `json:"limit"`
}

// milestoneTracksTool publishes the input contract via a hand-written schema, the
// same way the other tools do (the installed jsonschema-go infers neither defaults
// nor bounds from struct tags): owner/repo required, limit optional with a default
// and 1..100 bounds the SDK applies before the handler runs.
func milestoneTracksTool() *mcp.Tool {
	minLimit, maxLimit := 1.0, 100.0
	return &mcp.Tool{
		Name:        "milestone_tracks",
		Description: "Survey a GitHub repository's open milestones and return the within-milestone track structure operators encode in each milestone's description, as compact structured facts for the caller to render. For each open milestone, the parsed tracks in description order — each with its label, an optional raw status annotation (a bold run-in's parenthetical, e.g. \"critical-path\", uninterpreted), and its member issue numbers in order (each with a raw status token: \"~~\" for a struck/abandoned member, a checkbox marker char, or none). Tracks are recognized by manifest-declared markers (heading levels and/or bold run-in labels) with a prose-section label stoplist; a description with no track structure yields a milestone with no tracks — the common case — rather than an error. The milestone fetch marks the block unavailable (with a rate_limited/fetch_failed reason) on failure rather than failing the call, and the result-set limits (milestones listed, tracks per milestone, members per track) are surfaced, never silently truncated. The server extracts the structure; tier/cut-line ranking judgment stays caller-side.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"owner": {Type: "string", Description: "repository owner (user or org)"},
				"repo":  {Type: "string", Description: "repository name"},
				// The minimum and default are load-bearing: a listLimit of 0 empties every
				// list, so this bound keeps in.Limit — and every per-level cap — at 1+.
				"limit": {
					Type:        "integer",
					Description: "maximum number of items to list per reduction: milestones listed, tracks per milestone, and members per track",
					Default:     json.RawMessage("20"),
					Minimum:     &minLimit,
					Maximum:     &maxLimit,
				},
			},
			Required: []string{"owner", "repo"},
		},
	}
}

// milestoneTracksHandler resolves the repo's conventions, fetches its open
// milestones (with their descriptions), and reduces them to the within-milestone
// track facts. The milestone fetch is the only fetch; its failure degrades the
// block to unavailable rather than failing the call (mirroring the orientation
// tool's milestone block). A manifest error names a file, so it is logged to stderr
// and replaced with a repo-named message. Identity (repo, generatedAt) and the
// rate-limit budget are stamped here, not in the pure reduction.
func milestoneTracksHandler(resolver *manifest.Resolver, fetcher github.Fetcher, now func() time.Time) mcp.ToolHandlerFor[milestoneTracksInput, summary.MilestoneTracksFacts] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in milestoneTracksInput) (*mcp.CallToolResult, summary.MilestoneTracksFacts, error) {
		owner, repo := strings.TrimSpace(in.Owner), strings.TrimSpace(in.Repo)
		if owner == "" || repo == "" {
			return nil, summary.MilestoneTracksFacts{}, fmt.Errorf("owner and repo are required")
		}
		ownerRepo := owner + "/" + repo

		cfg, _, err := resolver.Resolve(ownerRepo)
		if err != nil {
			log.Printf("overstory: milestone_tracks: manifest resolution for %s: %v", ownerRepo, err)
			return nil, summary.MilestoneTracksFacts{}, fmt.Errorf("manifest configuration error for %s", ownerRepo)
		}

		facts, budget := milestoneTracksReduce(ctx, fetcher, ownerRepo, cfg.MilestoneTracks, in.Limit, now)
		facts.Repo = ownerRepo
		facts.GeneratedAt = now()
		facts.RateLimit = mapRateLimit(budget)
		return nil, facts, nil
	}
}

// milestoneTracksReduce runs the milestone fetch and reduces it, degrading to an
// unavailable block on failure the same way summaryMilestones does: a rate limit
// names its reason and returns a zero-remaining budget carrying the reset instant;
// any other failure stays on stderr and returns a nil budget. Both degrade paths
// return a non-nil empty slice so the output renders [] rather than null.
func milestoneTracksReduce(ctx context.Context, fetcher github.Fetcher, ownerRepo string, cfg manifest.MilestoneTracksConfig, limit int, now func() time.Time) (summary.MilestoneTracksFacts, *github.RateLimit) {
	res, err := fetcher.ListOpenMilestones(ctx, ownerRepo, cfg.FetchLimit)
	if err == nil {
		truncated := len(res.Milestones) < res.TotalOpen
		return summary.ReduceMilestoneTracks(res.Milestones, res.TotalOpen, truncated, mapTrackParams(cfg), limit), res.RateLimit
	}
	if rle, ok := errors.AsType[github.RateLimitedError](err); ok {
		return summary.MilestoneTracksFacts{Available: false, Unavailable: "rate_limited", Milestones: []summary.MilestoneTrackSet{}},
			&github.RateLimit{Remaining: 0, ResetAt: rateLimitResetTime(rle, now)}
	}
	log.Printf("overstory: milestone_tracks fetch for %s: %v", ownerRepo, err)
	return summary.MilestoneTracksFacts{Available: false, Unavailable: "fetch_failed", Milestones: []summary.MilestoneTrackSet{}}, nil
}

// mapTrackParams adapts the manifest's milestone-track convention to the reduction's
// params, keeping the reduction layer decoupled from convention resolution (the same
// decoupling mapPrefixes/mapQuality provide).
func mapTrackParams(cfg manifest.MilestoneTracksConfig) summary.TrackParams {
	return summary.TrackParams{
		HeadingLevels: cfg.HeadingLevels,
		BoldRunIn:     cfg.BoldRunIn,
		LabelStoplist: cfg.LabelStoplist,
	}
}

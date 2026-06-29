package server

import (
	"encoding/json"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// topLevelKeys decodes the tool's structured content into the set of top-level
// JSON keys actually on the wire, so a projection test asserts a block's
// presence or absence against the delivered bytes — not a re-decode into a typed
// struct, which cannot distinguish an omitted key from a zero value.
func topLevelKeys(t *testing.T, res *mcp.CallToolResult) map[string]json.RawMessage {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal top-level keys: %v", err)
	}
	return m
}

// assertKeys fails unless every wanted key is present and every unwanted key is
// absent, naming the offender so a projection regression is legible.
func assertKeys(t *testing.T, keys map[string]json.RawMessage, present, absent []string) {
	t.Helper()
	for _, k := range present {
		if _, ok := keys[k]; !ok {
			t.Errorf("block %q absent, want present", k)
		}
	}
	for _, k := range absent {
		if _, ok := keys[k]; ok {
			t.Errorf("block %q present, want absent (not requested)", k)
		}
	}
}

// backlogProjectionManifest declares deferred labels and a critical path so every
// projectable block is meaningfully populated in a full-composite response.
const backlogProjectionManifest = "acme/widgets:\n" +
	"  staleness:\n    thresholdDays: 30\n" +
	"  deferred:\n    labels: [deferred]\n" +
	"  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"/\"\n" +
	"  criticalPath:\n    streams: [simulation]\n    label: critical-path\n"

func backlogProjectionFetcher() fakeFetcher {
	return fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			deferredIssue(1, daysAgo(100), "deferred"),
			labeledIssue(2, "area/simulation", "critical-path"),
			titledIssue(3, "a fixable rendering bug"),
		},
		TotalOpen: 3,
	}}
}

// TestBacklogReviewProjectionSubset pins the core contract: requesting one block
// returns that block plus the always-present meta blocks, and omits every other
// content block from the wire. The successful (non-error) call also proves the
// generated output schema accepts omitted blocks (the omitempty-pointer linchpin).
func TestBacklogReviewProjectionSubset(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	srv := New(WithFetcher(backlogProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"deferred"}})
	keys := topLevelKeys(t, res)

	assertKeys(t, keys,
		[]string{"deferred", "openIssueSet", "repo", "generatedAt"},
		[]string{"staleness", "areaBalance", "quality", "overlap", "crossRef", "trajectory", "prTrajectory", "criticalPath"},
	)
}

// TestBacklogReviewProjectionStalenessDecoupledFromDeferred guards the cross-block
// coupling trap: staleness is reduced with the deferred-exclusion set, which is a
// staleness dependency, not the deferred block. Requesting staleness WITHOUT
// deferred must still exclude deferred issues — identical numbers to the full
// composite — or projection would silently corrupt the staleness block.
func TestBacklogReviewProjectionStalenessDecoupledFromDeferred(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  staleness:\n    thresholdDays: 30\n  deferred:\n    labels: [deferred]\n")
	fetcher := fakeFetcher{result: github.IssueListResult{
		Issues: []github.Issue{
			deferredIssue(1, daysAgo(100), "deferred"), // deferred + inactive → excluded
			deferredIssue(2, daysAgo(50)),              // plain stale
			deferredIssue(3, daysAgo(5), "deferred"),   // deferred + active → excluded
			deferredIssue(4, daysAgo(5)),               // plain fresh
		},
		TotalOpen: 4,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"staleness"}})
	assertKeys(t, topLevelKeys(t, res), []string{"staleness"}, []string{"deferred"})

	s := decodeFacts(t, res).Staleness
	if s == nil {
		t.Fatal("staleness block nil after requesting it")
	}
	if s.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1 (deferred issues excluded even though deferred not requested)", s.StaleCount)
	}
	if s.DeferredExcludedCount != 2 {
		t.Errorf("DeferredExcludedCount = %d, want 2 (deferredNums computed regardless of the deferred block)", s.DeferredExcludedCount)
	}
}

// TestBacklogReviewProjectionMultiBlock pins that a multi-name allowlist returns
// exactly those blocks.
func TestBacklogReviewProjectionMultiBlock(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	srv := New(WithFetcher(backlogProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"staleness", "quality"}})
	assertKeys(t, topLevelKeys(t, res),
		[]string{"staleness", "quality"},
		[]string{"deferred", "areaBalance", "overlap", "crossRef", "trajectory", "prTrajectory", "criticalPath"},
	)
}

// TestBacklogReviewProjectionDefaultFullComposite pins that an absent blocks
// parameter returns every content block — the default that keeps existing callers
// unaffected.
func TestBacklogReviewProjectionDefaultFullComposite(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	srv := New(WithFetcher(backlogProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})
	assertKeys(t, topLevelKeys(t, res), backlogBlockNames, nil)
}

// TestBacklogReviewProjectionEmptyBlocksIsFullComposite pins that an explicit
// empty array is treated as "all", same as an absent parameter.
func TestBacklogReviewProjectionEmptyBlocksIsFullComposite(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	srv := New(WithFetcher(backlogProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{}})
	assertKeys(t, topLevelKeys(t, res), backlogBlockNames, nil)
}

// TestBacklogReviewProjectionSkipsSecondaryFetch pins the fan-out rate-limit win:
// a primary-only allowlist runs neither trajectory fetch (and does not panic
// despite the now-pointer blocks).
func TestBacklogReviewProjectionSkipsSecondaryFetch(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	f := backlogProjectionFetcher()
	f.activityCalls = &atomic.Int64{}
	f.prActivityCalls = &atomic.Int64{}
	srv := New(WithFetcher(f), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"deferred"}})
	assertKeys(t, topLevelKeys(t, res), []string{"deferred"}, []string{"trajectory", "prTrajectory"})

	if n := f.activityCalls.Load(); n != 0 {
		t.Errorf("issue-trajectory fetch ran %d times, want 0 (trajectory not requested)", n)
	}
	if n := f.prActivityCalls.Load(); n != 0 {
		t.Errorf("pr-trajectory fetch ran %d times, want 0 (prTrajectory not requested)", n)
	}
}

// TestBacklogReviewProjectionRunsRequestedSecondaryFetch is the positive control:
// requesting a secondary block runs exactly its fetch and returns the block.
func TestBacklogReviewProjectionRunsRequestedSecondaryFetch(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	f := backlogProjectionFetcher()
	f.activityCalls = &atomic.Int64{}
	f.prActivityCalls = &atomic.Int64{}
	srv := New(WithFetcher(f), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"trajectory"}})
	assertKeys(t, topLevelKeys(t, res), []string{"trajectory"}, []string{"prTrajectory", "deferred"})

	if n := f.activityCalls.Load(); n != 1 {
		t.Errorf("issue-trajectory fetch ran %d times, want 1", n)
	}
	if n := f.prActivityCalls.Load(); n != 0 {
		t.Errorf("pr-trajectory fetch ran %d times, want 0 (prTrajectory not requested)", n)
	}
}

// TestBacklogReviewProjectionRejectsUnknownBlock pins that an unrecognized block
// name is an actionable tool error, never a silent near-empty success.
func TestBacklogReviewProjectionRejectsUnknownBlock(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	srv := New(WithFetcher(backlogProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	res := callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets", "blocks": []any{"nope"}})
	if !res.IsError {
		t.Fatalf("expected tool error for unknown block name, got success: %s", contentText(res))
	}
}

// TestBacklogReviewBlockNameTagBijection guards drift between the block-name
// constants (which drive the schema enum and the projection set) and the actual
// JSON keys: every constant must be a real projectable key, and every projectable
// key must have a constant. Meta keys are excluded by name.
func TestBacklogReviewBlockNameTagBijection(t *testing.T) {
	root := writeManifestDir(t, backlogProjectionManifest)
	srv := New(WithFetcher(backlogProjectionFetcher()), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	keys := topLevelKeys(t, callBacklogReview(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	assertBijection(t, keys, backlogBlockNames)
}

// metaBlockKeys are the always-present (or omitempty meta) top-level keys that are
// not projectable and so carry no block-name constant.
var metaBlockKeys = map[string]bool{
	"repo": true, "generatedAt": true, "openIssueSet": true, "rateLimit": true, "sizeBound": true,
}

func assertBijection(t *testing.T, keys map[string]json.RawMessage, blockNames []string) {
	t.Helper()
	want := map[string]bool{}
	for _, n := range blockNames {
		want[n] = true
	}
	var nonMeta []string
	for k := range keys {
		if !metaBlockKeys[k] {
			nonMeta = append(nonMeta, k)
		}
	}
	sort.Strings(nonMeta)
	for _, k := range nonMeta {
		if !want[k] {
			t.Errorf("projectable JSON key %q has no block-name constant", k)
		}
	}
	for _, n := range blockNames {
		if _, ok := keys[n]; !ok {
			t.Errorf("block-name constant %q is not a top-level JSON key", n)
		}
	}
}

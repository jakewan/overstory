package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/summary"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callMilestoneTracks drives the tool through the in-memory MCP session and
// returns the raw result so error-path cases can assert on IsError.
func callMilestoneTracks(t *testing.T, srv *mcp.Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	cs := connect(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "milestone_tracks",
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call milestone_tracks: %v", err)
	}
	return res
}

// decodeMilestoneTracks round-trips StructuredContent back into the typed facts.
func decodeMilestoneTracks(t *testing.T, res *mcp.CallToolResult) summary.MilestoneTracksFacts {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var facts summary.MilestoneTracksFacts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("unmarshal facts: %v", err)
	}
	return facts
}

// TestMilestoneTracksParsesTracks drives the tool surface end-to-end: a milestone
// whose description carries the operator's real track shapes — a stoplisted prose
// heading, a heading-container over bold-run-in tracks, inline members, checkbox
// members, a struck member, and a PR reference — must reduce to ordered tracks
// with ordered members and captured status tokens. Defaults supply the markers
// (headingLevels [2,3], boldRunIn) and the stoplist, so a bare manifest entry
// exercises them.
func TestMilestoneTracksParsesTracks(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	desc := "## Ikigai\n\n" +
		"Prose mentioning #999 that must not become a track.\n\n" +
		"## Active tracks\n\n" +
		"**Foundation** (critical-path): #10 #11\n\n" +
		"**Picker UX** (depends on Foundation):\n\n" +
		"- [x] #20 — done — PR #500\n" +
		"- [ ] #21 — pending\n" +
		"- [x] ~~#22~~ — superseded\n"
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{
			{Number: 7, Title: "M12", URL: "u7", OpenIssues: 5, ClosedIssues: 1, Description: desc},
		},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))

	if !facts.Available {
		t.Fatalf("Available = false, want true; Unavailable=%q", facts.Unavailable)
	}
	if facts.Repo != "acme/widgets" {
		t.Errorf("Repo = %q, want acme/widgets", facts.Repo)
	}
	if facts.OpenMilestones != 1 || facts.FetchTruncated {
		t.Errorf("OpenMilestones=%d FetchTruncated=%v, want 1/false", facts.OpenMilestones, facts.FetchTruncated)
	}
	if len(facts.Milestones) != 1 {
		t.Fatalf("got %d milestone sets, want 1", len(facts.Milestones))
	}
	ms := facts.Milestones[0]
	if ms.Number != 7 || ms.Title != "M12" {
		t.Errorf("milestone identity = %d/%q, want 7/M12", ms.Number, ms.Title)
	}
	// `## Ikigai` is stoplisted (its #999 prose mention is not a member); `## Active
	// tracks` is a container with no direct member; the two bold-run-ins are tracks.
	if len(ms.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2 (Foundation, Picker UX); tracks=%+v", len(ms.Tracks), ms.Tracks)
	}
	foundation := ms.Tracks[0]
	if foundation.Label != "Foundation" || foundation.Status != "critical-path" {
		t.Errorf("track[0] = %q/%q, want Foundation/critical-path", foundation.Label, foundation.Status)
	}
	if got := memberNumbers(foundation.Members); !equalInts(got, []int{10, 11}) {
		t.Errorf("Foundation members = %v, want [10 11]", got)
	}
	picker := ms.Tracks[1]
	if picker.Label != "Picker UX" || picker.Status != "depends on Foundation" {
		t.Errorf("track[1] = %q/%q, want Picker UX/depends on Foundation", picker.Label, picker.Status)
	}
	// PR #500 is excluded; the three issue members survive in order with their tokens.
	if got := memberNumbers(picker.Members); !equalInts(got, []int{20, 21, 22}) {
		t.Fatalf("Picker UX members = %v, want [20 21 22] (PR #500 excluded)", got)
	}
	if picker.Members[0].StatusToken != "x" {
		t.Errorf("#20 token = %q, want x (checked)", picker.Members[0].StatusToken)
	}
	if picker.Members[1].StatusToken != "" {
		t.Errorf("#21 token = %q, want empty (unchecked, conflated with inline)", picker.Members[1].StatusToken)
	}
	if picker.Members[2].StatusToken != "~~" {
		t.Errorf("#22 token = %q, want ~~ (struck/abandoned, not live)", picker.Members[2].StatusToken)
	}
}

// TestMilestoneTracksProseDescriptionYieldsNoTracks confirms the common case:
// a description with no markers reduces to a milestone with zero tracks, cleanly,
// never an error — the overwhelmingly common shape across real repos.
func TestMilestoneTracksProseDescriptionYieldsNoTracks(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{
			{Number: 3, Title: "v1.0", URL: "u3", OpenIssues: 4, Description: "Issues to resolve for v1.0. Tracking epic: #5."},
		},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if !facts.Available {
		t.Fatalf("Available = false, want true")
	}
	if len(facts.Milestones) != 1 {
		t.Fatalf("got %d milestone sets, want 1", len(facts.Milestones))
	}
	if len(facts.Milestones[0].Tracks) != 0 {
		t.Errorf("Tracks = %+v, want none (prose description)", facts.Milestones[0].Tracks)
	}
}

// TestMilestoneTracksSurfacesDescription pins the theme passthrough end-to-end
// (#32): the verbatim milestone description reaches the client alongside the
// parsed tracks, so a render can show the milestone's stated purpose.
func TestMilestoneTracksSurfacesDescription(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	desc := "## Ikigai\n\nMake the picker delightful.\n\n**Foundation** (anchor): #1"
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{
			{Number: 7, Title: "M12", URL: "u7", OpenIssues: 2, Description: desc},
		},
		TotalOpen: 1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Milestones) != 1 {
		t.Fatalf("got %d milestone sets, want 1", len(facts.Milestones))
	}
	if facts.Milestones[0].Description != desc {
		t.Errorf("Description = %q, want the verbatim description %q", facts.Milestones[0].Description, desc)
	}
}

// TestMilestoneTracksDegradesOnFetchFailure pins the degradation seam: a milestone
// fetch failure marks the block unavailable with a fetch_failed reason and a
// non-nil empty slice, rather than failing the whole call.
func TestMilestoneTracksDegradesOnFetchFailure(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	fetcher := fakeFetcher{milestonesErr: github.ErrRepoNotFound} // a non-rate-limit failure
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if facts.Available {
		t.Errorf("Available = true, want false on fetch failure")
	}
	if facts.Unavailable != "fetch_failed" {
		t.Errorf("Unavailable = %q, want fetch_failed", facts.Unavailable)
	}
	if facts.Milestones == nil {
		t.Errorf("Milestones = nil, want non-nil empty slice (renders [] not null)")
	}
}

func memberNumbers(ms []summary.TrackMember) []int {
	out := make([]int, len(ms))
	for i, m := range ms {
		out[i] = m.Number
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

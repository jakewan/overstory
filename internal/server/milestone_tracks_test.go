package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
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

// TestMilestoneTracksCarriesForeignMembersSeparately pins #154 on the track read. A
// reference into another repository (or a fork) is never a local member — members
// holds this repository's issues only, so a consumer that reads nothing else cannot
// render the foreign number as a local issue. It travels in externalMembers with its
// qualifier, its decoration, and its position: how many local members preceded it,
// which is what lets a renderer restore the operator's order. A reference qualified
// with the repository's own name is local, and a track holding only foreign
// references is still a track.
func TestMilestoneTracksCarriesForeignMembersSeparately(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	desc := "## Auth rework\n\n" +
		"- [x] #12\n" +
		"- [ ] ~~acme-lib/sso.js#40~~\n" +
		"- [ ] #15\n" +
		"- [ ] Acme/Widgets#16\n\n" +
		"## Vendor\n\n" +
		"- [x] bob#3\n"
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{{Number: 7, Title: "M12", URL: "u7", Description: desc}},
		TotalOpen:  1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Milestones) != 1 {
		t.Fatalf("got %d milestone sets, want 1", len(facts.Milestones))
	}
	tracks := facts.Milestones[0].Tracks
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2 (Auth rework, Vendor); tracks=%+v", len(tracks), tracks)
	}
	auth := tracks[0]
	if got := memberNumbers(auth.Members); !equalInts(got, []int{12, 15, 16}) {
		t.Errorf("Auth members = %v, want [12 15 16] (foreign excluded, self-qualified kept)", got)
	}
	wantAuth := []summary.ExternalTrackMember{{
		ForeignRef:  reduce.ForeignRef{Repo: "acme-lib/sso.js", Number: 40},
		StatusToken: "~~",
		Position:    1,
	}}
	if !reflect.DeepEqual(auth.ExternalMembers, wantAuth) {
		t.Errorf("Auth externalMembers = %+v, want %+v", auth.ExternalMembers, wantAuth)
	}
	vendor := tracks[1]
	if vendor.Label != "Vendor" || len(vendor.Members) != 0 {
		t.Errorf("track[1] = %q with members %v, want Vendor with no local members", vendor.Label, memberNumbers(vendor.Members))
	}
	if vendor.Members == nil {
		t.Error("Vendor members = nil (serialized as null), want non-nil empty slice")
	}
	wantVendor := []summary.ExternalTrackMember{{
		ForeignRef:  reduce.ForeignRef{ForkOwner: "bob", Number: 3},
		StatusToken: "x",
		Position:    0,
	}}
	if !reflect.DeepEqual(vendor.ExternalMembers, wantVendor) {
		t.Errorf("Vendor externalMembers = %+v, want %+v", vendor.ExternalMembers, wantVendor)
	}
}

// TestMilestoneTracksReadsIssueURLsAndLinkTargets pins the URL half of #154's fix
// on the one surface that arrives as raw markdown: an issue URL or a link to one is
// a member, placed by its target — so a link whose text reads `#5` but points into
// another repository is an external member rather than local #5 — and a URL a
// boundary orphans is counted in unassignedRefs rather than lost.
func TestMilestoneTracksReadsIssueURLsAndLinkTargets(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets: {}\n")
	desc := "## Auth\n\n" +
		"- [ ] [#5](https://github.com/other/lib/issues/5)\n" +
		"- [x] [SSO rework](https://github.com/acme/widgets/issues/7)\n" +
		"- [ ] https://github.com/other/lib/pull/9\n\n" +
		"# Notes\n\n" +
		"See https://github.com/other/lib/issues/11.\n"
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{{Number: 7, Title: "M12", URL: "u7", Description: desc}},
		TotalOpen:  1,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if len(facts.Milestones) != 1 || len(facts.Milestones[0].Tracks) != 1 {
		t.Fatalf("milestones=%+v, want one milestone with one track", facts.Milestones)
	}
	ms := facts.Milestones[0]
	auth := ms.Tracks[0]
	if got := memberNumbers(auth.Members); !equalInts(got, []int{7}) || auth.Members[0].StatusToken != "x" {
		t.Errorf("Auth members = %+v, want [7] checked (a link into this repository)", auth.Members)
	}
	wantExternal := []summary.ExternalTrackMember{{ForeignRef: reduce.ForeignRef{Repo: "other/lib", Number: 5}, Position: 0}}
	if !reflect.DeepEqual(auth.ExternalMembers, wantExternal) {
		t.Errorf("Auth externalMembers = %+v, want %+v (link read by its target; PR URL excluded)", auth.ExternalMembers, wantExternal)
	}
	if ms.UnassignedRefs != 1 {
		t.Errorf("UnassignedRefs = %d, want 1 (the orphaned issue URL under # Notes)", ms.UnassignedRefs)
	}
}

// TestMilestoneTracksHeadingsBoundTracksTheyDoNotStart drives #121 through the
// manifest: with headingLevels emptied, headings no longer start tracks but must
// still end them, so a narrative section's references stay out of the preceding
// track's members. The two milestones split the reporting decision the default
// stoplist governs — the reporter's own shape ends on a stoplisted `## Summary`,
// which the operator declared prose, so it reports nothing; an unlisted narrative
// label reports the orphan. This is the only case exercising a non-default
// milestoneTracks entry end-to-end.
func TestMilestoneTracksHeadingsBoundTracksTheyDoNotStart(t *testing.T) {
	root := writeManifestDir(t, "acme/widgets:\n  milestoneTracks:\n    headingLevels: []\n")
	stoplisted := "## Tracks\n\n" +
		"**Alpha** (critical-path): #1\n\n" +
		"## Summary\n\n" +
		"Follow-up context in #99.\n"
	unlisted := "## Tracks\n\n" +
		"**Beta** (parallel): #2\n\n" +
		"## Follow-ups\n\n" +
		"Loose end in #98.\n"
	fetcher := fakeFetcher{milestones: github.MilestoneListResult{
		Milestones: []github.Milestone{
			{Number: 7, Title: "M12", URL: "u7", OpenIssues: 2, Description: stoplisted},
			{Number: 8, Title: "M13", URL: "u8", OpenIssues: 2, Description: unlisted},
		},
		TotalOpen: 2,
	}}
	srv := New(WithFetcher(fetcher), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

	facts := decodeMilestoneTracks(t, callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"}))
	if !facts.Available {
		t.Fatalf("Available = false, want true; Unavailable=%q", facts.Unavailable)
	}
	if len(facts.Milestones) != 2 {
		t.Fatalf("got %d milestone sets, want 2", len(facts.Milestones))
	}
	for _, tc := range []struct {
		set            summary.MilestoneTrackSet
		label          string
		member         int
		unassignedRefs int
	}{
		{facts.Milestones[0], "Alpha", 1, 0},
		{facts.Milestones[1], "Beta", 2, 1},
	} {
		t.Run(tc.label, func(t *testing.T) {
			if len(tc.set.Tracks) != 1 {
				t.Fatalf("got %d tracks, want 1 (only the bold run-in): %+v", len(tc.set.Tracks), tc.set.Tracks)
			}
			got := tc.set.Tracks[0].Members
			if len(got) != 1 || got[0].Number != tc.member {
				t.Errorf("%s members = %+v, want only #%d (the trailing section sits past the boundary)", tc.label, got, tc.member)
			}
			if tc.set.UnassignedRefs != tc.unassignedRefs {
				t.Errorf("UnassignedRefs = %d, want %d", tc.set.UnassignedRefs, tc.unassignedRefs)
			}
		})
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

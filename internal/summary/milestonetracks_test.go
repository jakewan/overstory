package summary

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// defaultTrackParams mirrors the manifest defaults (headings 2/3, bold run-in on)
// with a small stoplist, so the parser cases read against realistic settings.
func defaultTrackParams() TrackParams {
	return TrackParams{
		HeadingLevels: []int{2, 3},
		BoldRunIn:     true,
		LabelStoplist: []string{"Ikigai", "Why", "History"},
	}
}

// renderTracks flattens parsed tracks to a compact, comparable form:
// "label|status|num:token,num:token; …".
func renderTracks(tracks []Track) string {
	parts := make([]string, len(tracks))
	for i, tr := range tracks {
		members := make([]string, len(tr.Members))
		for j, m := range tr.Members {
			members[j] = fmt.Sprintf("%d:%s", m.Number, m.StatusToken)
		}
		parts[i] = fmt.Sprintf("%s|%s|%s", tr.Label, tr.Status, strings.Join(members, ","))
	}
	return strings.Join(parts, "; ")
}

func TestParseTracks(t *testing.T) {
	for _, tc := range []struct {
		name string
		desc string
		want string
	}{
		{
			name: "bold run-in with inline members",
			desc: "**Foundation** (critical-path): #10 #11",
			want: "Foundation|critical-path|10:,11:",
		},
		{
			name: "bold run-in with checkbox members and tokens",
			desc: "**Picker UX** (depends on Foundation):\n- [x] #20\n- [ ] #21\n- [x] ~~#22~~",
			want: "Picker UX|depends on Foundation|20:x,21:,22:~~",
		},
		{
			name: "heading with numbered members",
			desc: "## Vocabularies\n7. #592\n8. #593",
			want: "Vocabularies||592:,593:",
		},
		{
			name: "heading container over bold-run-in tracks",
			desc: "## Active tracks\n\n**Diversity** (parallel): #750",
			want: "Diversity|parallel|750:",
		},
		{
			name: "stoplisted heading swallows its prose mentions",
			desc: "## Ikigai\n\nprose mentioning #999\n\n**Foundation** (anchor): #1",
			want: "Foundation|anchor|1:",
		},
		{
			name: "stoplisted bold run-in is not a track",
			desc: "**Why**: unblocks #132\n\n**Foundation** (anchor): #1",
			want: "Foundation|anchor|1:",
		},
		{
			name: "bolded issue number is a member not a marker",
			desc: "**Foundation** (x):\n- [x] **#823** — done",
			want: "Foundation|x|823:x",
		},
		{
			name: "fenced code is skipped",
			desc: "```\n## NotATrack\n- [x] #5\n```\n**Real** (x): #1",
			want: "Real|x|1:",
		},
		{
			name: "indented code is skipped",
			desc: "**Real** (x): #1\n\n    ## NotATrack\n    - [x] #5",
			want: "Real|x|1:",
		},
		{
			name: "pull-request references are excluded",
			desc: "**T** (x): #1, PR #2 (#3)",
			want: "T|x|1:,3:",
		},
		// Refactor guards (#32): the strikethrough/PR-exclusion logic reads the text
		// before each ref via the byte index the shared reduce.IssueRefMatches helper
		// returns. These pin the multi-ref-per-line cases the original suite omitted.
		{
			name: "two struck members on one line",
			desc: "**T** (x): ~~#1~~ ~~#2~~",
			want: "T|x|1:~~,2:~~",
		},
		{
			name: "struck member followed by a live member on the same line",
			desc: "**T** (x): ~~#1~~ #2",
			want: "T|x|1:~~,2:",
		},
		{
			name: "PR reference immediately preceding a real reference",
			desc: "**T** (x): PR #1 #2",
			want: "T|x|2:",
		},
		{
			name: "prose without markers yields no tracks",
			desc: "Issues to resolve for v1.0. Tracking epic: #5.",
			want: "",
		},
		{
			name: "container heading with no members yields no track",
			desc: "## Active tracks\n\nsome prose, no refs",
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracks, _ := parseTracks(tc.desc, defaultTrackParams(), 100)
			if got := renderTracks(tracks); got != tc.want {
				t.Errorf("parseTracks =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

func TestParseTracksAllMarkersDisabledYieldsNoTracks(t *testing.T) {
	params := TrackParams{HeadingLevels: nil, BoldRunIn: false}
	tracks, _ := parseTracks("## Heading\n#5\n**Bold** (x): #6", params, 100)
	if len(tracks) != 0 {
		t.Errorf("tracks = %s, want none (all markers disabled)", renderTracks(tracks))
	}
}

func TestParseTracksTruncatesMembersAndTracks(t *testing.T) {
	// Two tracks, three members each, listLimit 2: the track list and each member
	// list cap, and both truncation flags are set.
	desc := "**A** (x): #1 #2 #3\n**B** (y): #4 #5 #6\n**C** (z): #7 #8 #9"
	tracks, listTruncated := parseTracks(desc, defaultTrackParams(), 2)
	if !listTruncated {
		t.Error("track list not flagged truncated, want true (3 tracks capped to 2)")
	}
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(tracks))
	}
	if len(tracks[0].Members) != 2 || !tracks[0].ListTruncated {
		t.Errorf("track A members=%d truncated=%v, want 2/true", len(tracks[0].Members), tracks[0].ListTruncated)
	}
}

// TestReduceMilestoneTracksCarriesDescription pins the theme passthrough (#32):
// the verbatim milestone description is surfaced alongside the parsed tracks so a
// client can render the milestone's stated theme/purpose.
func TestReduceMilestoneTracksCarriesDescription(t *testing.T) {
	desc := "## Ikigai\n\nShip the picker.\n\n**Foundation** (anchor): #1"
	facts := ReduceMilestoneTracks([]github.Milestone{{Number: 1, Description: desc}}, 1, false, defaultTrackParams(), 20)
	if len(facts.Milestones) != 1 {
		t.Fatalf("got %d milestones, want 1", len(facts.Milestones))
	}
	if facts.Milestones[0].Description != desc {
		t.Errorf("Description = %q, want the verbatim milestone description %q", facts.Milestones[0].Description, desc)
	}
}

func TestReduceMilestoneTracksSurfacesFetchSeamAndSortsByNumber(t *testing.T) {
	ms := []github.Milestone{
		{Number: 9, Title: "later", Description: "**A** (x): #1"},
		{Number: 4, Title: "earlier", Description: "prose only"},
	}
	// fetchTruncated true, totalOpen 5 > fetched 2: the seam must surface.
	facts := ReduceMilestoneTracks(ms, 5, true, defaultTrackParams(), 20)
	if !facts.Available {
		t.Fatal("Available = false, want true")
	}
	if facts.OpenMilestones != 5 || facts.FetchedCount != 2 || !facts.FetchTruncated {
		t.Errorf("seam = open %d fetched %d truncated %v, want 5/2/true", facts.OpenMilestones, facts.FetchedCount, facts.FetchTruncated)
	}
	if len(facts.Milestones) != 2 || facts.Milestones[0].Number != 4 {
		t.Fatalf("milestones not sorted by number ascending: %+v", facts.Milestones)
	}
	// The prose milestone is present with an empty (non-nil) track list.
	if facts.Milestones[0].Tracks == nil {
		t.Error("prose milestone Tracks = nil, want non-nil empty slice")
	}
	if len(facts.Milestones[1].Tracks) != 1 {
		t.Errorf("milestone 9 tracks = %d, want 1", len(facts.Milestones[1].Tracks))
	}
}

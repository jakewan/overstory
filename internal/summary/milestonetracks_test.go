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
		// params overrides defaultTrackParams for the cases that turn on a
		// non-default marker set; nil keeps the realistic defaults.
		params *TrackParams
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
		// Heading boundaries (#121). A heading bounds a track whether or not its level
		// starts one, so membership no longer shifts when headingLevels is narrowed.
		{
			name:   "narrative heading bounds a bold-run-in track when no level starts tracks",
			desc:   "## Tracks\n\n**Alpha** (critical-path): #1\n\n## Summary\n\nFollow-up context in #99.",
			want:   "Alpha|critical-path|1:",
			params: &TrackParams{HeadingLevels: []int{}, BoldRunIn: true, LabelStoplist: []string{"Ikigai", "Why", "History"}},
		},
		{
			name: "sub-heading below a bold run-in stays content",
			desc: "## Tracks\n**Alpha**:\n#### Members\n- [x] #1",
			want: "Alpha||1:x",
		},
		{
			name:   "sub-heading below a heading-started track stays content",
			desc:   "## A\n### Sub\n- #1",
			want:   "A||1:",
			params: &TrackParams{HeadingLevels: []int{2}, BoldRunIn: true},
		},
		{
			name:   "shallower heading ends a deeper track",
			desc:   "### Track\n- #1\n## Section\n- #2",
			want:   "Track||1:",
			params: &TrackParams{HeadingLevels: []int{3}, BoldRunIn: true},
		},
		{
			name: "h1 ends a default-level track",
			desc: "## A\n- #1\n# Top\n- #2",
			want: "A||1:",
		},
		{
			name:   "bold run-in at document root ends at the first heading",
			desc:   "**Alpha**: #1\n## Notes\nmore in #2",
			want:   "Alpha||1:",
			params: &TrackParams{HeadingLevels: []int{3}, BoldRunIn: true},
		},
		{
			name: "two consecutive member-carrying headings",
			desc: "## A\n#1\n## B\n#2",
			want: "A||1:; B||2:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := defaultTrackParams()
			if tc.params != nil {
				params = *tc.params
			}
			tracks, _, _ := parseTracks(tc.desc, params, 100)
			if got := renderTracks(tracks); got != tc.want {
				t.Errorf("parseTracks =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestParseTracksCountsUnassignedRefs pins the seam that keeps a boundary heading
// from dropping references silently (#121): a reference the parser matched inside
// the track region but could not assign to a track is counted. A reference the
// operator deliberately excluded — prose before any marker, or prose under a
// stoplisted label — is not, so the count stays zero on a well-configured repo.
func TestParseTracksCountsUnassignedRefs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		desc      string
		want      int
		listLimit int
		params    *TrackParams
	}{
		{
			name:   "reference orphaned by a boundary heading counts",
			desc:   "### T\n- #1\n## Notes\n- #2",
			want:   1,
			params: &TrackParams{HeadingLevels: []int{3}, BoldRunIn: true},
		},
		{
			name: "prose references before the first marker do not count",
			desc: "Tracking epic: #5\n\n## A\n- #1",
			want: 0,
		},
		{
			name: "references under a stoplisted label do not count",
			desc: "## Ikigai\n\nprose mentioning #999\n\n**Foundation** (anchor): #1",
			want: 0,
		},
		{
			name: "a description with no markers counts nothing",
			desc: "Issues to resolve for v1.0. Tracking epic: #5.",
			want: 0,
		},
		{
			name:   "pull-request references are never counted",
			desc:   "### T\n- #1\n## Notes\nPR #2 and #3",
			want:   1,
			params: &TrackParams{HeadingLevels: []int{3}, BoldRunIn: true},
		},
		{
			name:      "members dropped by the list cap do not count",
			desc:      "**A** (x): #1 #2 #3",
			want:      0,
			listLimit: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := defaultTrackParams()
			if tc.params != nil {
				params = *tc.params
			}
			limit := tc.listLimit
			if limit == 0 {
				limit = 100
			}
			if _, _, got := parseTracks(tc.desc, params, limit); got != tc.want {
				t.Errorf("unassigned refs = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParseTracksAllMarkersDisabledYieldsNoTracks(t *testing.T) {
	params := TrackParams{HeadingLevels: nil, BoldRunIn: false}
	tracks, _, _ := parseTracks("## Heading\n#5\n**Bold** (x): #6", params, 100)
	if len(tracks) != 0 {
		t.Errorf("tracks = %s, want none (all markers disabled)", renderTracks(tracks))
	}
}

func TestParseTracksTruncatesMembersAndTracks(t *testing.T) {
	// Two tracks, three members each, listLimit 2: the track list and each member
	// list cap, and both truncation flags are set.
	desc := "**A** (x): #1 #2 #3\n**B** (y): #4 #5 #6\n**C** (z): #7 #8 #9"
	tracks, listTruncated, _ := parseTracks(desc, defaultTrackParams(), 2)
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

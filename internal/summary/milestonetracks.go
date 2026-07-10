package summary

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// MilestoneTracksFacts is the within-milestone track block: each open milestone's
// parsed track structure, the priority ordering operators encode in the milestone
// description. Available is false only when the milestone fetch failed; the block
// then degrades rather than failing the whole call, and Unavailable names the
// reason. OpenMilestones is the repository's exact open-milestone count and
// FetchTruncated marks a milestone fetch that did not cover them all, so a capped
// fetch never silently omits milestones. Repo, GeneratedAt, RateLimit, and SizeBound
// are stamped by the handler, not the reduction (the reduction is a pure function of
// the descriptions).
//
// SizeBound is present only when the assembled response exceeded the byte budget and
// had to be trimmed. The trim sheds per-track member lists only; unlike the composite
// tools it cannot shed the verbatim per-milestone Description ("unbounded by design",
// below), so the bound is best-effort — SizeBound.FinalBytes honestly reports an
// overflow when the description floor alone exceeds the budget.
type MilestoneTracksFacts struct {
	Repo           string                 `json:"repo"`
	GeneratedAt    time.Time              `json:"generatedAt"`
	Available      bool                   `json:"available"`
	Unavailable    string                 `json:"unavailable,omitempty"`
	OpenMilestones int                    `json:"openMilestones"`
	FetchedCount   int                    `json:"fetchedCount"`
	FetchTruncated bool                   `json:"fetchTruncated"`
	Milestones     []MilestoneTrackSet    `json:"milestones"`
	Limit          int                    `json:"limit"`
	ListTruncated  bool                   `json:"listTruncated"`
	RateLimit      *reduce.RateLimitFacts `json:"rateLimit,omitempty"`
	SizeBound      *reduce.SizeBoundFacts `json:"sizeBound,omitempty"`
}

// MilestoneTrackSet is one open milestone's parsed tracks. Tracks is empty (not
// nil) when the description carries no recognizable track structure — the common
// case, and a clean result rather than an error. ListTruncated marks a track list
// capped at the list limit.
//
// Description is the verbatim milestone description, surfaced for theme/prose
// rendering (the milestone's stated purpose lives only here). Tracks is the
// authoritative member structure — it applies the stoplist, PR-exclusion,
// fenced-code skipping, and member cap — so a client must not re-parse
// Description for membership, or its counts will diverge from this reduction.
// Unbounded by design: a milestone description is operator-authored planning
// prose, so it carries no truncation seam like the other list-shaped fields.
type MilestoneTrackSet struct {
	Number        int     `json:"number"`
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Description   string  `json:"description,omitempty"`
	Tracks        []Track `json:"tracks"`
	ListTruncated bool    `json:"listTruncated"`
}

// Track is one parsed track: its Label (the marker text), an optional Status (a
// bold-run-in's raw parenthetical, e.g. "critical-path", uninterpreted), and its
// Members in description order. ListTruncated marks a member list capped at the
// list limit — parse-relative ("parsed more than emitted"), since the description
// is the only source and there is no authoritative member count to compare against.
// The response size bound also sets it when it trims this member list to fit the byte
// budget; the dropped/remaining split then lives in the sizeBound marker's
// TrimmedBlock, since Track carries no count field of its own.
type Track struct {
	Label         string        `json:"label"`
	Status        string        `json:"status,omitempty"`
	Members       []TrackMember `json:"members"`
	ListTruncated bool          `json:"listTruncated"`
}

// TrackMember is one referenced issue within a track. StatusToken is the raw,
// uninterpreted structural decoration on the reference: "~~" for a struck
// (abandoned) member, the checkbox marker char ("x", "X", "→") for a marked
// checkbox item, or empty for an unchecked box or an inline reference. Strikethrough
// wins over a checkbox, so a struck member never reads as live.
type TrackMember struct {
	Number      int    `json:"number"`
	StatusToken string `json:"statusToken,omitempty"`
}

// TrackParams is the resolved track convention the reduction consumes, decoupled
// from the manifest layer (the handler adapts manifest.MilestoneTracksConfig into
// it, the same way the area/quality reductions take primitives). HeadingLevels are
// the heading depths that start a track; BoldRunIn enables `**Label** (status):`
// markers; LabelStoplist names prose-section labels that are not tracks.
type TrackParams struct {
	HeadingLevels []int
	BoldRunIn     bool
	LabelStoplist []string
}

var (
	// A markdown heading at column 0 (up to three leading spaces): the hashes and
	// the label after the required space.
	headingRe = regexp.MustCompile(`^ {0,3}(#{1,6})\s+(.*)$`)
	// A bold run-in label with optional parenthetical, then a colon:
	// `**Label** (status):`. The label group is non-greedy up to the closing `**`.
	boldRunInRe = regexp.MustCompile(`^\s*\*\*\s*([^*]+?)\s*\*\*\s*(\([^)]*\))?\s*:`)
	// A task-list item, capturing the single character inside the checkbox brackets.
	checkboxRe = regexp.MustCompile(`^\s*[-*+]\s+\[(.)\]`)
	// A fenced-code delimiter line (``` or ~~~). Toggles fence state; double-tilde
	// strikethrough (`~~x~~`) is not a fence (it is inline, and needs three for a
	// fence anyway).
	fenceRe = regexp.MustCompile("^\\s*(```|~~~)")
)

// ReduceMilestoneTracks parses each milestone's description into its track
// structure as compact facts. It is a pure function of the descriptions: no clock
// (no time-derived field) and no issue fetch — Repo, GeneratedAt, and RateLimit are
// stamped by the handler. totalOpenMilestones keeps OpenMilestones exact when the
// milestone fetch truncates (fetchTruncated). listLimit caps the milestone list,
// the tracks per milestone, and the members per track, each flagged independently.
func ReduceMilestoneTracks(milestones []github.Milestone, totalOpenMilestones int, fetchTruncated bool, params TrackParams, listLimit int) MilestoneTracksFacts {
	facts := MilestoneTracksFacts{
		Available:      true,
		OpenMilestones: totalOpenMilestones,
		FetchedCount:   len(milestones),
		FetchTruncated: fetchTruncated,
		Limit:          listLimit,
	}

	sets := make([]MilestoneTrackSet, 0, len(milestones))
	for _, m := range milestones {
		tracks, trackListTruncated := parseTracks(m.Description, params, listLimit)
		sets = append(sets, MilestoneTrackSet{
			Number:        m.Number,
			Title:         m.Title,
			URL:           m.URL,
			Description:   m.Description,
			Tracks:        tracks,
			ListTruncated: trackListTruncated,
		})
	}
	// By number for a stable order independent of fetch ordering, like the
	// milestone-progress block.
	sort.Slice(sets, func(i, j int) bool { return sets[i].Number < sets[j].Number })
	if listLimit >= 0 && len(sets) > listLimit {
		facts.ListTruncated = true
		sets = sets[:listLimit]
	}
	facts.Milestones = sets
	return facts
}

// parseTracks extracts the ordered tracks from one milestone description. A track
// is a marker (a heading at a configured level, or a bold run-in label when
// enabled) whose label is not stoplisted and that carries at least one issue
// reference before the next marker — so a container heading with no direct members
// and a prose-section label both yield no track. It returns the tracks and whether
// the track list was capped at listLimit.
func parseTracks(desc string, params TrackParams, listLimit int) ([]Track, bool) {
	levels := make(map[int]bool, len(params.HeadingLevels))
	for _, l := range params.HeadingLevels {
		levels[l] = true
	}
	stop := make(map[string]bool, len(params.LabelStoplist))
	for _, s := range params.LabelStoplist {
		stop[strings.ToLower(strings.TrimSpace(s))] = true
	}

	tracks := make([]Track, 0)
	var cur *Track // open track candidate; nil means we are in a non-track section
	flush := func() {
		// A candidate is a real track only if it gathered a member: this is the
		// "≥1 reference before the next marker" rule that drops container headings.
		if cur != nil && len(cur.Members) > 0 {
			tracks = append(tracks, *cur)
		}
		cur = nil
	}

	inFence := false
	for _, line := range strings.Split(desc, "\n") {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		// Fenced and indented code is content, not structure: skip it so a pasted
		// example block never parses as a track.
		if inFence || isIndentedCode(line) {
			continue
		}

		if m := headingRe.FindStringSubmatch(line); m != nil && levels[len(m[1])] {
			flush()
			label := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m[2]), "#"))
			if !stop[strings.ToLower(label)] {
				cur = &Track{Label: label}
			}
			continue
		}

		if params.BoldRunIn {
			if m := boldRunInRe.FindStringSubmatchIndex(line); m != nil {
				label := strings.TrimSpace(line[m[2]:m[3]])
				// A bold span that is itself an issue number (`**#823**`) is a member, not
				// a track label — let it fall through to member extraction.
				if !strings.HasPrefix(label, "#") {
					flush()
					if !stop[strings.ToLower(label)] {
						status := ""
						if m[4] >= 0 {
							status = strings.TrimSpace(strings.Trim(line[m[4]:m[5]], "()"))
						}
						cur = &Track{Label: label, Status: status}
						// Members written inline after the colon belong to this track.
						addMembers(cur, line[m[1]:])
					}
					continue
				}
			}
		}

		// A non-marker line contributes its references to the open track, if any.
		if cur != nil {
			addMembers(cur, line)
		}
	}
	flush()

	listTruncated := false
	if listLimit >= 0 {
		for i := range tracks {
			if len(tracks[i].Members) > listLimit {
				tracks[i].Members = tracks[i].Members[:listLimit]
				tracks[i].ListTruncated = true
			}
		}
		if len(tracks) > listLimit {
			tracks = tracks[:listLimit]
			listTruncated = true
		}
	}
	return tracks, listTruncated
}

// addMembers appends the issue references found in text to the track, in
// appearance order, skipping pull-request references. Each member's StatusToken is
// the raw structural decoration in its context: a strikethrough wrap wins ("~~"),
// else a task-list checkbox marker char, else empty.
func addMembers(t *Track, text string) {
	checkboxChar := ""
	hasCheckbox := false
	if cb := checkboxRe.FindStringSubmatch(text); cb != nil {
		hasCheckbox = true
		if c := cb[1]; strings.TrimSpace(c) != "" {
			checkboxChar = c
		}
	}
	// reduce.IssueRefMatches handles the #N scan and PR-reference exclusion (the
	// shared convention); the per-member decoration is read from the text before
	// each reference, which ref.Start locates.
	for _, ref := range reduce.IssueRefMatches(text) {
		pre := text[:ref.Start]
		token := ""
		switch {
		case strings.Count(pre, "~~")%2 == 1:
			// An odd number of strikethrough delimiters before the reference means it
			// sits inside a `~~…~~` span: a struck, abandoned member.
			token = "~~"
		case hasCheckbox:
			token = checkboxChar
		}
		t.Members = append(t.Members, TrackMember{Number: ref.Number, StatusToken: token})
	}
}

// isIndentedCode reports a markdown indented-code line: a leading tab or four
// leading spaces. Nested list items in real milestones indent by two, so they are
// not treated as code; a four-space-deep nest is, an accepted edge of the
// structural-extraction approach.
func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
}

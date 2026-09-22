package reduce

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// issueRefRe matches an issue reference: the `#` and digits, optionally preceded
	// by the qualifier GitHub links as another repository's issue — `owner/repo#N`
	// (groups 1–2) or the owner-only fork form `owner#N` (group 3). The character sets
	// follow GitHub's naming rules: an owner is letters and digits joined by single
	// hyphens, so it neither starts nor ends with one (GitHub links the `#12` in
	// `re-#12` as local); a repository name also allows `.` and `_`. Leftmost-first
	// matching takes the last owner/repo pair before the `#`, as GitHub's linker does
	// (`x/anthropics/claude-code#1` links anthropics/claude-code), because a pair that
	// is followed by `/` rather than `#` fails and the scan moves on. Pull-request
	// references are excluded separately by inspecting the preceding text — this
	// pattern alone cannot tell `#5` from `PR #5`.
	issueRefRe = regexp.MustCompile(`(?:([A-Za-z0-9]+(?:-[A-Za-z0-9]+)*)/([A-Za-z0-9._-]+)|([A-Za-z0-9]+(?:-[A-Za-z0-9]+)*))?#(\d+)`)
	// prContextRe marks a pull-request reference to exclude by the text immediately
	// before the reference — before its qualifier, when it has one, so a repository
	// named `my-pr` is not mistaken for the marker: a word-boundaried `PR`/`PR `
	// (so "expr" does not match) or a `pull/` path segment.
	prContextRe = regexp.MustCompile(`(?i)(\bpr\s*|pull/)$`)
	// issueURLRe matches a GitHub issue or pull-request URL — owner (group 1),
	// repository (2), `issues` or `pull` (3), number (4) — which names its issue as
	// surely as a qualified reference does. GitHub shortens such a URL to its display
	// form (`owner/repo#N`, or `#N` for this repository) in bodyText, so the pattern
	// matters where no rendering has happened: raw milestone markdown, and a URL
	// quoted in a code span. A trailing fragment (`#issuecomment-…`) is left
	// unmatched and holds no digits after its `#`, so it adds no reference.
	issueURLRe = regexp.MustCompile(`(?i)` + issueURLPattern)
	// issueLinkRe matches a markdown inline link whose target is such a URL, with the
	// same groups. GitHub links it to its target whatever its text says, so the text
	// is not scanned: `[#5](…/other/repo/issues/5)` names other/repo#5, not local #5.
	issueLinkRe = regexp.MustCompile(`(?i)\[[^\]\n]*\]\(<?` + issueURLPattern + `[^)\n]*\)`)
)

const issueURLPattern = `https?://(?:www\.)?github\.com/([A-Za-z0-9]+(?:-[A-Za-z0-9]+)*)/([A-Za-z0-9._-]+)/(issues|pull)/(\d+)`

// IssueRef is one issue reference found in text: its issue Number and the byte
// Start of the reference — its qualifier when it has one, else its '#' — so a caller
// can inspect the text before the reference (for strikethrough or checkbox
// decoration) or tell whether text opens with one, without re-scanning. Foreign is
// nil for a reference to the surveyed repository and names the other repository, or
// the fork owner, otherwise.
type IssueRef struct {
	Number  int
	Start   int
	Foreign *ForeignRef
}

// ForeignRef is a reference written in text that names an issue outside the
// surveyed repository: Repo for `owner/repo#N`, ForkOwner for GitHub's owner-only
// `owner#N`. Exactly one is set. GitHub resolves the owner-only form to that owner's
// fork of the surveyed repository whatever the fork is named (observed through its
// markdown render API; the form is not in GitHub's documented reference list), so
// the owner is carried alone rather than paired with a guessed repository name.
//
// It records what the text says, not what exists: the issue may be closed, a pull
// request, or absent altogether — a string in a code span GitHub never linked, or a
// word glued to a `#` that no fork answers to. So unlike ExternalRef it is never a
// gate.
type ForeignRef struct {
	Repo      string `json:"repo,omitempty"`
	ForkOwner string `json:"forkOwner,omitempty"`
	Number    int    `json:"number"`
}

// qualifier is the text that places a foreign reference — its repository, or its
// fork owner — which is what dedup and ordering compare.
func (f ForeignRef) qualifier() string {
	if f.Repo != "" {
		return f.Repo
	}
	return f.ForkOwner
}

// IssueRefMatches returns the issue references in text in appearance order, with
// pull-request references (one preceded by a PR marker, e.g. `PR #5` or a `pull/#5`
// path segment) excluded and any number too large for int skipped. self is the
// surveyed repository as `owner/repo`: a reference qualified with it, or with its
// owner alone, is local, compared case-insensitively as GitHub does; every other
// qualified reference is Foreign. An empty self makes every qualified reference
// Foreign. It does not dedup: callers that need appearance order and per-reference
// context (e.g. milestone tracks, which decorate each member) read the raw sequence.
// BodyRefs is the deduped, sorted convenience over it.
//
// A GitHub issue URL, or a markdown link whose target is one, is a reference too,
// placed by its target like a qualified one; a pull-request URL is excluded like
// any pull-request reference. Only the target is read: GitHub links a link to its
// target, so `#N` in the link's text is not also a reference.
func IssueRefMatches(text, self string) []IssueRef {
	selfOwner, _, _ := strings.Cut(self, "/")
	out := make([]IssueRef, 0)
	// claimed holds the byte spans the URL passes already read, so the #N scan does
	// not read a link's text (or anything inside a URL) a second time.
	var claimed [][2]int
	inClaimed := func(pos int) bool {
		for _, c := range claimed {
			if pos >= c[0] && pos < c[1] {
				return true
			}
		}
		return false
	}
	addURL := func(text string, loc []int) {
		claimed = append(claimed, [2]int{loc[0], loc[1]})
		if strings.EqualFold(text[loc[6]:loc[7]], "pull") {
			return
		}
		num, err := strconv.Atoi(text[loc[8]:loc[9]])
		if err != nil {
			return
		}
		ref := IssueRef{Number: num, Start: loc[0]}
		if repo := text[loc[2]:loc[5]]; self == "" || !strings.EqualFold(repo, self) {
			ref.Foreign = &ForeignRef{Repo: repo, Number: num}
		}
		out = append(out, ref)
	}
	for _, loc := range issueLinkRe.FindAllStringSubmatchIndex(text, -1) {
		addURL(text, loc)
	}
	for _, loc := range issueURLRe.FindAllStringSubmatchIndex(text, -1) {
		if !inClaimed(loc[0]) {
			addURL(text, loc)
		}
	}
	urlRefs := len(out)

	for _, loc := range issueRefRe.FindAllStringSubmatchIndex(text, -1) {
		refStart, numStart, numEnd := loc[0], loc[8], loc[9]
		if inClaimed(refStart) || inClaimed(numStart) {
			continue
		}
		if prContextRe.MatchString(text[:refStart]) {
			continue
		}
		num, err := strconv.Atoi(text[numStart:numEnd])
		if err != nil {
			continue // a number too large for int is not a usable reference
		}
		ref := IssueRef{Number: num, Start: refStart}
		switch {
		case loc[2] >= 0:
			repo := text[loc[2]:loc[5]]
			if self == "" || !strings.EqualFold(repo, self) {
				ref.Foreign = &ForeignRef{Repo: repo, Number: num}
			}
		case loc[6] >= 0:
			owner := text[loc[6]:loc[7]]
			// `PR#5` has the fork-owner shape but is the pull-request marker, which the
			// context check above cannot see because the marker is the match itself.
			if strings.EqualFold(owner, "pr") {
				continue
			}
			if selfOwner == "" || !strings.EqualFold(owner, selfOwner) {
				ref.Foreign = &ForeignRef{ForkOwner: owner, Number: num}
			}
		}
		out = append(out, ref)
	}
	if urlRefs > 0 {
		// The passes each run in appearance order; merging restores it across them.
		sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	}
	return out
}

// BodyRefs splits the references in text into an issue's stated dependencies: the
// distinct local numbers, ascending, with exclude dropped (an issue citing its own
// number is never a dependency, so a reduction passes the issue's own number), and
// the distinct foreign references, ordered by qualifier and then number. Both
// compare qualifiers case-insensitively and keep the first spelling seen, since text
// GitHub did not render keeps its author's casing. Both are non-nil even when empty,
// so a caller embedding them serializes [] rather than null.
func BodyRefs(text, self string, exclude int) ([]int, []ForeignRef) {
	matches := IssueRefMatches(text, self)
	local := make([]int, 0, len(matches))
	external := make([]ForeignRef, 0)
	seenLocal := make(map[int]bool, len(matches))
	seenForeign := make(map[ForeignRef]bool)
	for _, m := range matches {
		if m.Foreign == nil {
			if m.Number == exclude || seenLocal[m.Number] {
				continue
			}
			seenLocal[m.Number] = true
			local = append(local, m.Number)
			continue
		}
		key := ForeignRef{
			Repo:      strings.ToLower(m.Foreign.Repo),
			ForkOwner: strings.ToLower(m.Foreign.ForkOwner),
			Number:    m.Number,
		}
		if seenForeign[key] {
			continue
		}
		seenForeign[key] = true
		external = append(external, *m.Foreign)
	}
	sort.Ints(local)
	sort.Slice(external, func(i, j int) bool {
		qi, qj := strings.ToLower(external[i].qualifier()), strings.ToLower(external[j].qualifier())
		if qi != qj {
			return qi < qj
		}
		return external[i].Number < external[j].Number
	})
	return local, external
}

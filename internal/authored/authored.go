// Package authored is the per-repo authored-activity reduction: given one
// repository, one author login, and a bounded time window, it carries the
// objective, decomposed counts of what that author created and engaged with —
// commits, issues opened, pull requests opened, reviews submitted, pull requests
// engaged (commented, not authored), and issues engaged. It is a per-repo
// measure primitive for a cross-project attention audit; deciding which repos to
// measure, how to weight the counts, and the attention verdict are the caller's
// job. The server reduces; the caller renders.
//
// Each count ships with its own fidelity label, because the categories are not
// equally precise: commit counts are event-precise (within their default-branch
// and identity-linkage limits), while the search-derived counts are
// index-approximate and window-approximate. The label travels with the number so
// a consumer never reads an approximate count as ground truth.
package authored

import (
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// Per-category fidelity labels. They state each count's known blind spots in
// plain English so the consumer can weight them honestly rather than treat every
// number as exact. The engagement and authored-search categories share the search
// index's lag; commits carry their own attribution limits.
const (
	fidelityCommits  = "default-branch commits whose author is identity-linked to this user; misses squash-merged and email-unlinked commits"
	fidelityOpened   = "search-index count; approximate and lags for recent windows"
	fidelityReviews  = "peer reviews on others' PRs active in the window (excludes the author's own PRs); approximate versus exact review dates"
	fidelityPREngage = "comment engagement on PRs active in the window; may miss inline review-thread comments"
	fidelityIsEngage = "comment engagement on issues active in the window"
)

// Facts is the authored-activity reduction's output: review-level identity (the
// repo, the author, the window, and the generation time) plus the decomposed
// counts. Repo and GeneratedAt describe the whole read and are stamped by the
// server handler, mirroring the other tools; the reduction fills the author,
// window, and counts. RateLimit is the fetch's budget snapshot, omitted when none
// was observed.
type Facts struct {
	Repo        string                 `json:"repo"`
	Author      string                 `json:"author"`
	Since       time.Time              `json:"since"`
	Until       time.Time              `json:"until"`
	GeneratedAt time.Time              `json:"generatedAt"`
	Counts      Counts                 `json:"counts"`
	RateLimit   *reduce.RateLimitFacts `json:"rateLimit,omitempty"`
}

// Counts is the decomposed authored/engagement counts, kept separate (never
// summed) so the consumer owns any weighting. Each is a Count carrying the number
// and its fidelity label.
type Counts struct {
	CommitsAuthored     Count `json:"commitsAuthored"`
	IssuesOpened        Count `json:"issuesOpened"`
	PullRequestsOpened  Count `json:"pullRequestsOpened"`
	ReviewsSubmitted    Count `json:"reviewsSubmitted"`
	PullRequestsEngaged Count `json:"pullRequestsEngaged"`
	IssuesEngaged       Count `json:"issuesEngaged"`
}

// Count is one category's number paired with its fidelity label, so the meaning
// of the count never travels separately from the count.
type Count struct {
	Count    int    `json:"count"`
	Fidelity string `json:"fidelity"`
}

// Reduce assembles the authored-activity facts from a fetched result, stamping
// each count with its per-category fidelity label and echoing the author and
// window. The window is normalized to UTC so the echoed bounds match the instants
// the fetch actually used. It is pure: Repo, GeneratedAt, and RateLimit are
// stamped by the caller.
func Reduce(result github.AuthoredActivityResult, author string, since, until time.Time) Facts {
	return Facts{
		Author: author,
		Since:  since.UTC(),
		Until:  until.UTC(),
		Counts: countsFrom(result),
	}
}

// countsFrom stamps each fetched count with its per-category fidelity label. It
// is shared by Reduce (single repo) and ReduceBatch (per-repo entry) so the two
// surfaces can never diverge on what a count means.
func countsFrom(result github.AuthoredActivityResult) Counts {
	return Counts{
		CommitsAuthored:     Count{Count: result.CommitsAuthored, Fidelity: fidelityCommits},
		IssuesOpened:        Count{Count: result.IssuesOpened, Fidelity: fidelityOpened},
		PullRequestsOpened:  Count{Count: result.PullRequestsOpened, Fidelity: fidelityOpened},
		ReviewsSubmitted:    Count{Count: result.ReviewsSubmitted, Fidelity: fidelityReviews},
		PullRequestsEngaged: Count{Count: result.PullRequestsEngaged, Fidelity: fidelityPREngage},
		IssuesEngaged:       Count{Count: result.IssuesEngaged, Fidelity: fidelityIsEngage},
	}
}

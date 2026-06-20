package authored

import (
	"strings"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// TestReduceMapsCountsAndStampsFidelity pins that each fetched count lands on its
// named category with a non-empty, category-specific fidelity label, and that the
// window/author echo through normalized to UTC.
func TestReduceMapsCountsAndStampsFidelity(t *testing.T) {
	result := github.AuthoredActivityResult{
		CommitsAuthored:     12,
		IssuesOpened:        3,
		PullRequestsOpened:  5,
		ReviewsSubmitted:    7,
		PullRequestsEngaged: 9,
		IssuesEngaged:       4,
	}
	// A non-UTC window must echo back normalized so the bounds match the instants
	// the fetch used.
	loc := time.FixedZone("UTC-5", -5*60*60)
	since := time.Date(2026, 5, 1, 0, 0, 0, 0, loc)
	until := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)

	facts := Reduce(result, "alice", since, until)

	if facts.Author != "alice" {
		t.Errorf("Author = %q, want alice", facts.Author)
	}
	if facts.Since.Location() != time.UTC || facts.Until.Location() != time.UTC {
		t.Errorf("window not normalized to UTC: since=%v until=%v", facts.Since, facts.Until)
	}
	if !facts.Since.Equal(since) || !facts.Until.Equal(until) {
		t.Errorf("window instants changed: since=%v until=%v", facts.Since, facts.Until)
	}

	for _, tc := range []struct {
		name string
		got  Count
		want int
	}{
		{"commitsAuthored", facts.Counts.CommitsAuthored, 12},
		{"issuesOpened", facts.Counts.IssuesOpened, 3},
		{"pullRequestsOpened", facts.Counts.PullRequestsOpened, 5},
		{"reviewsSubmitted", facts.Counts.ReviewsSubmitted, 7},
		{"pullRequestsEngaged", facts.Counts.PullRequestsEngaged, 9},
		{"issuesEngaged", facts.Counts.IssuesEngaged, 4},
	} {
		if tc.got.Count != tc.want {
			t.Errorf("%s count = %d, want %d", tc.name, tc.got.Count, tc.want)
		}
		if strings.TrimSpace(tc.got.Fidelity) == "" {
			t.Errorf("%s carries no fidelity label", tc.name)
		}
	}

	// Commits carry a different fidelity story (event-precise but attribution-
	// limited) than the search-derived counts; the labels must not be uniform.
	if facts.Counts.CommitsAuthored.Fidelity == facts.Counts.IssuesOpened.Fidelity {
		t.Errorf("commit and search fidelity labels are identical; they describe different precision")
	}
}

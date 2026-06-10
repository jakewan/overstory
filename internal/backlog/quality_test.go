package backlog

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// bodyIssue builds an issue with a body and labels for the quality reduction.
// CreatedAt comes from the shared builder (ago(400)), so AgeDays is deterministic.
func bodyIssue(num int, body string, labels ...string) github.Issue {
	is := labeledIssue(num, 0, labels...)
	is.BodyText = body
	return is
}

var typeCategory = []Category{{Name: "type", Prefixes: []PrefixRule{{Prefix: "type", Delimiter: "/"}}}}

func TestReduceQualityFlagsEmptyBody(t *testing.T) {
	issues := []github.Issue{
		bodyIssue(1, "", "type/bug"),                 // empty body → flagged
		bodyIssue(2, "a real description", "type/x"), // fine
		bodyIssue(3, "   ", "type/y"),                // whitespace-only → flagged
	}
	facts := ReduceQuality(issues, 3, QualityParams{MinBodyLength: 1, Categories: typeCategory}, 20, now)
	if facts.MissingBodyCount != 2 {
		t.Errorf("MissingBodyCount = %d, want 2", facts.MissingBodyCount)
	}
	if facts.FlaggedCount != 2 {
		t.Errorf("FlaggedCount = %d, want 2 (issue 2 passes all checks)", facts.FlaggedCount)
	}
}

func TestReduceQualityBodyThresholdBoundary(t *testing.T) {
	// Predicate is BodyLength < MinBodyLength: 9 chars flagged at threshold 10, 10 not.
	issues := []github.Issue{
		bodyIssue(1, "123456789", "type/a"),  // len 9 → flagged
		bodyIssue(2, "1234567890", "type/b"), // len 10 → fine
	}
	facts := ReduceQuality(issues, 2, QualityParams{MinBodyLength: 10, Categories: typeCategory}, 20, now)
	if facts.MissingBodyCount != 1 {
		t.Errorf("MissingBodyCount = %d, want 1 (len 9 < 10, len 10 not)", facts.MissingBodyCount)
	}
}

func TestReduceQualityZeroMinBodyLengthDisablesBodyCheck(t *testing.T) {
	// MinBodyLength 0 disables the body check (a length is never < 0), but BodyLength
	// is still reported so a caller can surface thinness itself.
	issues := []github.Issue{bodyIssue(1, "", "type/a")}
	facts := ReduceQuality(issues, 1, QualityParams{MinBodyLength: 0, Categories: typeCategory}, 20, now)
	if facts.MissingBodyCount != 0 {
		t.Errorf("MissingBodyCount = %d, want 0 (body check disabled)", facts.MissingBodyCount)
	}
	if facts.FlaggedCount != 0 {
		t.Errorf("FlaggedCount = %d, want 0 (only check disabled)", facts.FlaggedCount)
	}
}

func TestReduceQualityFlagsNoLabels(t *testing.T) {
	issues := []github.Issue{
		bodyIssue(1, "a real description"),           // no labels → flagged
		bodyIssue(2, "a real description", "type/x"), // labeled → fine
	}
	facts := ReduceQuality(issues, 2, QualityParams{MinBodyLength: 1}, 20, now)
	if facts.NoLabelsCount != 1 {
		t.Errorf("NoLabelsCount = %d, want 1", facts.NoLabelsCount)
	}
	if facts.CategoriesConfigured {
		t.Error("CategoriesConfigured = true, want false (none declared)")
	}
}

func TestReduceQualityMissingRequiredCategory(t *testing.T) {
	cats := []Category{
		{Name: "type", Prefixes: []PrefixRule{{Prefix: "type", Delimiter: "/"}}},
		{Name: "priority", Prefixes: []PrefixRule{{Prefix: "priority", Delimiter: "/"}}},
	}
	issues := []github.Issue{
		bodyIssue(1, "desc", "type/bug", "priority/high"), // both → fine
		bodyIssue(2, "desc", "type/bug"),                  // missing priority
		bodyIssue(3, "desc", "other"),                     // missing both
	}
	facts := ReduceQuality(issues, 3, QualityParams{MinBodyLength: 1, Categories: cats}, 20, now)
	if !facts.CategoriesConfigured {
		t.Error("CategoriesConfigured = false, want true")
	}
	if facts.MissingCategoryCounts["type"] != 1 {
		t.Errorf("MissingCategoryCounts[type] = %d, want 1 (issue 3)", facts.MissingCategoryCounts["type"])
	}
	if facts.MissingCategoryCounts["priority"] != 2 {
		t.Errorf("MissingCategoryCounts[priority] = %d, want 2 (issues 2, 3)", facts.MissingCategoryCounts["priority"])
	}
	if facts.FlaggedCount != 2 {
		t.Errorf("FlaggedCount = %d, want 2 (issues 2, 3)", facts.FlaggedCount)
	}
}

func TestReduceQualityZeroLabelsCountedInNoLabelsAndEveryCategory(t *testing.T) {
	// A zero-label issue fails NoLabels and every configured category — per-check
	// counts overlap, so they need not sum to FlaggedCount (one flagged issue here).
	issues := []github.Issue{bodyIssue(1, "a real description")}
	facts := ReduceQuality(issues, 1, QualityParams{MinBodyLength: 1, Categories: typeCategory}, 20, now)
	if facts.NoLabelsCount != 1 {
		t.Errorf("NoLabelsCount = %d, want 1", facts.NoLabelsCount)
	}
	if facts.MissingCategoryCounts["type"] != 1 {
		t.Errorf("MissingCategoryCounts[type] = %d, want 1", facts.MissingCategoryCounts["type"])
	}
	if facts.FlaggedCount != 1 {
		t.Errorf("FlaggedCount = %d, want 1 (single issue, overlapping checks)", facts.FlaggedCount)
	}
	if len(facts.FlaggedIssues) != 1 {
		t.Fatalf("listed %d, want 1", len(facts.FlaggedIssues))
	}
	q := facts.FlaggedIssues[0]
	if !q.NoLabels || len(q.MissingCategories) != 1 || q.MissingCategories[0] != "type" {
		t.Errorf("issue facts = %+v, want NoLabels and MissingCategories [type]", q)
	}
}

func TestReduceQualityConfiguredCategoryWithNoMissesStillReported(t *testing.T) {
	// A configured category with zero misses must still appear (count 0), so a
	// caller sees the category was checked.
	issues := []github.Issue{bodyIssue(1, "desc", "type/bug")}
	facts := ReduceQuality(issues, 1, QualityParams{MinBodyLength: 1, Categories: typeCategory}, 20, now)
	if got, ok := facts.MissingCategoryCounts["type"]; !ok || got != 0 {
		t.Errorf("MissingCategoryCounts[type] = %d (present=%v), want 0/true", got, ok)
	}
	if len(facts.ConfiguredCategories) != 1 || facts.ConfiguredCategories[0] != "type" {
		t.Errorf("ConfiguredCategories = %v, want [type]", facts.ConfiguredCategories)
	}
}

func TestReduceQualitySortMostIncompleteFirst(t *testing.T) {
	cats := []Category{{Name: "type", Prefixes: []PrefixRule{{Prefix: "type", Delimiter: "/"}}}}
	issues := []github.Issue{
		bodyIssue(1, "desc", "type/bug"), // missing nothing → not flagged
		bodyIssue(2, "", "type/bug"),     // 1 fail (body)
		bodyIssue(3, ""),                 // 3 fails (body, noLabels, type)
	}
	facts := ReduceQuality(issues, 3, QualityParams{MinBodyLength: 1, Categories: cats}, 20, now)
	if len(facts.FlaggedIssues) != 2 {
		t.Fatalf("listed %d, want 2", len(facts.FlaggedIssues))
	}
	if facts.FlaggedIssues[0].Number != 3 || facts.FlaggedIssues[1].Number != 2 {
		t.Errorf("order = [%d,%d], want [3,2] (most-incomplete first)",
			facts.FlaggedIssues[0].Number, facts.FlaggedIssues[1].Number)
	}
}

func TestReduceQualityLimitCapsListNotCount(t *testing.T) {
	issues := []github.Issue{
		bodyIssue(1, ""), bodyIssue(2, ""), bodyIssue(3, ""),
	}
	facts := ReduceQuality(issues, 3, QualityParams{MinBodyLength: 1}, 2, now)
	if facts.FlaggedCount != 3 {
		t.Errorf("FlaggedCount = %d, want 3 (count uncapped)", facts.FlaggedCount)
	}
	if len(facts.FlaggedIssues) != 2 || !facts.ListTruncated {
		t.Errorf("listed %d truncated=%v, want 2/true", len(facts.FlaggedIssues), facts.ListTruncated)
	}
}

func TestReduceQualityExactOpenAndFetchTruncation(t *testing.T) {
	issues := []github.Issue{bodyIssue(1, "")}
	facts := ReduceQuality(issues, 500, QualityParams{MinBodyLength: 1}, 20, now)
	if facts.OpenIssueCount != 500 {
		t.Errorf("OpenIssueCount = %d, want 500 (exact)", facts.OpenIssueCount)
	}
	if facts.FetchedCount != 1 || !facts.FetchTruncated {
		t.Errorf("FetchedCount=%d FetchTruncated=%v, want 1/true", facts.FetchedCount, facts.FetchTruncated)
	}
}

func TestReduceQualityAgeDays(t *testing.T) {
	// CreatedAt is ago(400) from the shared builder.
	facts := ReduceQuality([]github.Issue{bodyIssue(1, "")}, 1, QualityParams{MinBodyLength: 1}, 20, now)
	if facts.FlaggedIssues[0].AgeDays != 400 {
		t.Errorf("AgeDays = %d, want 400", facts.FlaggedIssues[0].AgeDays)
	}
}

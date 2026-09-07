package reduce

import (
	"encoding/json"
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

// edges builds open native dependency edges to the given issue numbers.
func edges(nums ...int) []github.DependencyRef {
	refs := make([]github.DependencyRef, len(nums))
	for i, n := range nums {
		refs[i] = github.DependencyRef{Number: n, Open: true}
	}
	return refs
}

// TestReadiness walks the inputs a verdict rests on: the two seam states across every
// value they can take, the open blocked-by edges, the sub-issue gap, and the edge-list
// truncation flag. Both failure directions are pinned here — an unread seam must never
// read as ready, and a relationship the forge does not carry must never read as
// blocked — because collapsing either is how the two reductions that share this
// predicate previously came to disagree.
func TestReadiness(t *testing.T) {
	const laterState = github.SeamState("someLaterState")

	cases := []struct {
		name string
		is   github.Issue
		want Verdict
	}{
		{
			name: "zero-value seams with nothing open is ready",
			is:   github.Issue{},
			want: VerdictReady,
		},
		{
			name: "both seams read, nothing open, is ready",
			is:   github.Issue{BlockedByState: github.SeamAvailable, SubIssueGapState: github.SeamAvailable},
			want: VerdictReady,
		},
		{
			name: "an open blocked-by edge blocks",
			is:   github.Issue{BlockedBy: edges(7)},
			want: VerdictBlocked,
		},
		{
			name: "a closed blocked-by edge no longer gates",
			is:   github.Issue{BlockedBy: []github.DependencyRef{{Number: 7, Open: false}}},
			want: VerdictReady,
		},
		{
			name: "an open sub-issue gap blocks",
			is:   github.Issue{SubIssuesTotal: 3, SubIssuesCompleted: 1},
			want: VerdictBlocked,
		},
		{
			name: "a closed-out sub-issue summary does not gate",
			is:   github.Issue{SubIssuesTotal: 3, SubIssuesCompleted: 3},
			want: VerdictReady,
		},
		{
			// ApplyCapabilities rewrites the state and never the counts, so a repository
			// without hierarchy can still present a non-zero gap. Reading the counts alone
			// would call this blocked on the absence of a concept.
			name: "a gap the forge does not carry is not a gate",
			is: github.Issue{
				SubIssuesTotal: 3, SubIssuesCompleted: 1,
				SubIssueGapState: github.SeamNotApplicable,
			},
			want: VerdictReady,
		},
		{
			// An observed blocker outranks an unconfirmable one: the issue is known
			// blocked whatever else went unread.
			name: "an observed gap blocks even where the seam went unread",
			is: github.Issue{
				SubIssuesTotal: 3, SubIssuesCompleted: 1,
				SubIssueGapState: github.SeamUnavailable,
			},
			want: VerdictBlocked,
		},
		{
			name: "a capped edge list cannot confirm readiness",
			is:   github.Issue{BlockedByTruncated: true},
			want: VerdictProvisional,
		},
		{
			name: "a capped edge list that still shows a blocker is blocked, not provisional",
			is:   github.Issue{BlockedBy: edges(7), BlockedByTruncated: true},
			want: VerdictBlocked,
		},
		{
			name: "an unread blocked-by seam cannot confirm readiness",
			is:   github.Issue{BlockedByState: github.SeamUnavailable},
			want: VerdictProvisional,
		},
		{
			name: "a blocked-by relationship the forge lacks withholds nothing",
			is:   github.Issue{BlockedByState: github.SeamNotApplicable},
			want: VerdictReady,
		},
		{
			name: "an unread sub-issue seam cannot confirm readiness",
			is:   github.Issue{SubIssueGapState: github.SeamUnavailable},
			want: VerdictProvisional,
		},
		{
			name: "a hierarchy the forge lacks withholds nothing",
			is:   github.Issue{SubIssueGapState: github.SeamNotApplicable},
			want: VerdictReady,
		},
		{
			name: "neither relationship carried still leaves a verdict complete",
			is: github.Issue{
				BlockedByState:   github.SeamNotApplicable,
				SubIssueGapState: github.SeamNotApplicable,
			},
			want: VerdictReady,
		},
		{
			// The arms enumerate the states that leave a verdict standing, so a state
			// added later lands here rather than falling through to ready.
			name: "an unrecognized blocked-by state fails safe",
			is:   github.Issue{BlockedByState: laterState},
			want: VerdictProvisional,
		},
		{
			name: "an unrecognized sub-issue state fails safe",
			is:   github.Issue{SubIssueGapState: laterState},
			want: VerdictProvisional,
		},
		{
			name: "an unrecognized state does not soften an observed blocker",
			is:   github.Issue{BlockedBy: edges(7), SubIssueGapState: laterState},
			want: VerdictBlocked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Readiness(tc.is); got != tc.want {
				t.Errorf("Readiness = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSubIssueGate pins the gap rule on its own, because it is published as a field
// as well as consumed by the verdict: the dependency block ships it per issue, and the
// two must never be computed separately.
func TestSubIssueGate(t *testing.T) {
	cases := []struct {
		name string
		is   github.Issue
		want bool
	}{
		{
			name: "open children gate",
			is:   github.Issue{SubIssuesTotal: 2, SubIssuesCompleted: 0},
			want: true,
		},
		{
			name: "partially completed children still gate",
			is:   github.Issue{SubIssuesTotal: 5, SubIssuesCompleted: 4},
			want: true,
		},
		{
			name: "every child completed does not gate",
			is:   github.Issue{SubIssuesTotal: 5, SubIssuesCompleted: 5},
			want: false,
		},
		{
			name: "no children at all does not gate",
			is:   github.Issue{},
			want: false,
		},
		{
			// The gap witnesses open children only where children exist as a concept.
			name: "a gap the forge does not carry does not gate",
			is: github.Issue{
				SubIssuesTotal: 2, SubIssuesCompleted: 0,
				SubIssueGapState: github.SeamNotApplicable,
			},
			want: false,
		},
		{
			// An unread seam is a different failure from an absent relationship: the
			// children were observed, so they gate; the verdict handles the unread part.
			name: "an unread seam does not suppress an observed gap",
			is: github.Issue{
				SubIssuesTotal: 2, SubIssuesCompleted: 0,
				SubIssueGapState: github.SeamUnavailable,
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubIssueGate(tc.is); got != tc.want {
				t.Errorf("SubIssueGate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestVerdictMarshalsUnsetAsProvisional pins the fail-safe direction of the zero
// value. A projection that forgets the field is a defect, but the wire must not carry
// an empty verdict a caller's bands match nothing against, and it must not promote the
// issue either.
func TestVerdictMarshalsUnsetAsProvisional(t *testing.T) {
	got, err := json.Marshal(struct {
		Readiness Verdict `json:"readiness"`
	}{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"readiness":"provisional"}`; string(got) != want {
		t.Errorf("marshaled = %s, want %s", got, want)
	}
}

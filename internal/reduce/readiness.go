package reduce

import (
	"encoding/json"

	"github.com/jakewan/overstory/internal/github"
)

// Verdict is what a reduction concluded about whether an issue is actionable now.
//
// It is a string rather than an integer enum for the same reason github.SeamState is:
// the name is the wire contract, and a caller reading "provisional" needs no ordinal
// table to interpret it.
type Verdict string

const (
	// VerdictReady means nothing open gates the issue and every input the verdict
	// rests on was either read or does not exist here.
	VerdictReady Verdict = "ready"
	// VerdictBlocked means an open blocker was actually observed — a native
	// blocked-by edge, or open sub-issue children.
	VerdictBlocked Verdict = "blocked"
	// VerdictProvisional means the issue presents no open blocker but readiness could
	// not be confirmed, because an input the verdict rests on was capped or unread. An
	// empty result is not by itself evidence of readiness.
	VerdictProvisional Verdict = "provisional"
)

// MarshalJSON renders the unset zero value as VerdictProvisional rather than emitting
// an empty string. There is no legitimate empty verdict — every reduction that
// projects one sets it — so a zero here means a projection forgot the field, and the
// question is only which way that failure should land. Provisional is the fail-safe:
// an unset verdict is genuinely not a confirmation of readiness, so it renders as
// indeterminate instead of promoting the issue, matching how the classification below
// already treats a state it does not recognize.
//
// This is a backstop, not the guard. A projection that drops the field is a defect,
// and the reductions' tests assert every projected issue carries a real verdict.
func (v Verdict) MarshalJSON() ([]byte, error) {
	if v == "" {
		v = VerdictProvisional
	}
	return json.Marshal(string(v))
}

// SubIssueGate reports whether open sub-issue children still gate this issue.
//
// The sub-issue summary counts every child (all repositories, never capped), so the
// open-child gap witnesses a gate even when the windowed child list is empty. It is
// an upper bound that can read one high after a not-planned closure — erring toward
// over-reporting the gate, never toward false-ready.
//
// The gap witnesses a gate only where the forge carries the relationship at all:
// where it does not, the zero is the absence of a concept rather than the absence of
// children. That guard is why the state is tested and not just the counts —
// github.ApplyCapabilities rewrites the state and never the counts, so a repository
// without hierarchy can still present a non-zero gap.
//
// It is exported alongside Readiness because the gate is not only an input to the
// verdict: the dependency reduction also publishes it per issue. Recomputing it there
// would put the same rule in two places, which is the divergence both functions exist
// to prevent.
func SubIssueGate(is github.Issue) bool {
	return is.SubIssueGapState != github.SeamNotApplicable &&
		is.SubIssuesTotal-is.SubIssuesCompleted > 0
}

// Readiness classifies one issue as ready, blocked, or provisional.
//
// It is the single readiness decision every reduction asks. A second,
// independently-written copy is how two blocks of one response come to disagree about
// the same issue — the reserve calling an issue unready while the dependency block
// counts it ready — and holding the two in step by hand has already failed.
//
// The issue's seam states must already be reconciled against what the repository
// carries (github.ApplyCapabilities); every reduction here does that first, defensively,
// so a direct caller cannot skip it. That is also the limit of what sharing this
// predicate guarantees: two reductions agree because their handler passes both the
// same issues and the same Capabilities, not because anything in the types enforces
// it. A caller that fed two reductions different capabilities would reintroduce the
// disagreement one level up from where this closes it.
//
// The arms enumerate the states that leave a verdict standing rather than the ones
// that withhold, so a seam state added later lands in provisional rather than falling
// through to ready.
func Readiness(is github.Issue) Verdict {
	switch {
	case hasOpenDependency(is.BlockedBy) || SubIssueGate(is):
		return VerdictBlocked
	// Appears unblocked, but a capped edge list may hide an open blocker and an unread
	// seam may hide anything at all, so readiness cannot be confirmed. A seam the forge
	// does not carry is not a fault and withholds nothing, which is what Withholds
	// answers.
	case is.BlockedByTruncated || is.BlockedByState.Withholds() || is.SubIssueGapState.Withholds():
		return VerdictProvisional
	default:
		return VerdictReady
	}
}

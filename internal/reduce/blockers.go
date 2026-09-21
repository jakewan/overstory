package reduce

import (
	"sort"

	"github.com/jakewan/overstory/internal/github"
)

// OpenDependencyNumbers projects an issue's native dependency edges — in either
// direction — to the ascending, distinct numbers of the referenced issues still
// open. For a blocked-by set it is "what still gates this issue"; for a blocking
// set it is "which still-open issues this one gates". Closed edges are dropped (a
// closed issue no longer gates either way); the fetch layer already separated the
// cross-repository edges, so every ref here is a same-repo issue and a bare number
// addresses it unambiguously (OpenExternalDependencies projects the rest). The result
// is non-nil even when empty, so a reduction embedding it serializes [] rather than
// null — matching the bodyRefs convention the dependency signals share.
//
// This is the single open-edge projection both the backlog and summary reductions
// call for both directions, so the native dependency signal reads identically on
// both tools.
func OpenDependencyNumbers(refs []github.DependencyRef) []int {
	seen := make(map[int]bool, len(refs))
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		if !r.Open || seen[r.Number] {
			continue
		}
		seen[r.Number] = true
		out = append(out, r.Number)
	}
	sort.Ints(out)
	return out
}

// ExternalRef is one still-open dependency in another repository, as a reduction
// projects it: the repository that qualifies the number, and the number. It carries
// no open flag because the projection already filtered to open edges, matching how
// OpenDependencyNumbers returns bare numbers rather than refs.
type ExternalRef struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// OpenExternalDependencies is OpenDependencyNumbers for the edges into other
// repositories: the distinct still-open ones, ordered by repository then number so
// the projection is deterministic. A number alone cannot address them, so each ref
// keeps its repository. The result is non-nil even when empty, for the same
// serialization reason.
func OpenExternalDependencies(refs []github.ExternalDependencyRef) []ExternalRef {
	seen := make(map[ExternalRef]bool, len(refs))
	out := make([]ExternalRef, 0, len(refs))
	for _, r := range refs {
		ref := ExternalRef{Repo: r.Repo, Number: r.Number}
		if !r.Open || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// hasOpenDependency reports whether any edge in the set is still open. It answers the
// question Readiness asks — is anything gating this — without building the projection
// OpenDependencyNumbers returns, which allocates a map and a slice and sorts them.
// Every reduction already computes that projection for its own output field, so
// reusing it there would compute it twice per issue to reach a boolean.
func hasOpenDependency(refs []github.DependencyRef) bool {
	for _, r := range refs {
		if r.Open {
			return true
		}
	}
	return false
}

// hasOpenExternalDependency is hasOpenDependency over the cross-repository edges. It
// is a second function rather than a generic one because the two ref types share no
// interface, and a one-field interface for a boolean would cost more than it saves.
func hasOpenExternalDependency(refs []github.ExternalDependencyRef) bool {
	for _, r := range refs {
		if r.Open {
			return true
		}
	}
	return false
}

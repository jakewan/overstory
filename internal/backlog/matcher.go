package backlog

import "strings"

// PrefixRule identifies a label family by a prefix and delimiter: a label matches
// when it starts (case-insensitively) with prefix+delimiter, and the projected
// name is the remainder, trimmed. The delimiter is configurable because the
// real-world conventions are fragmented — `area/` (slash), `area:` (colon),
// `area-` (dash) all see heavy use with no dominant winner.
type PrefixRule struct {
	Prefix    string
	Delimiter string
}

// labelMatcher classifies an issue label against an explicit allow-list and a set
// of prefix rules, projecting each match to a canonical name. The explicit list
// takes precedence over prefixes for naming. It is the shared matching primitive
// behind the deferred and area-balance reductions.
type labelMatcher struct {
	labels   map[string]struct{} // normalized explicit labels
	prefixes []PrefixRule
}

// newLabelMatcher builds a matcher from an explicit label list and prefix rules;
// either may be empty (an empty matcher matches nothing).
func newLabelMatcher(labels []string, prefixes []PrefixRule) labelMatcher {
	set := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		set[normalizeLabel(l)] = struct{}{}
	}
	return labelMatcher{labels: set, prefixes: prefixes}
}

// match reports whether label matches a configured area and, if so, its canonical
// name. Both paths return the original-cased label trimmed of surrounding
// whitespace, so the projected name is consistent with the whitespace-insensitive
// matching: an explicit-list match returns the trimmed label (callers echoing it
// keep the original casing); a prefix match returns the suffix after the
// delimiter, trimmed. A prefix whose suffix is empty (a bare "area/" label) does
// not match — it would otherwise manufacture a blank-named area.
func (m labelMatcher) match(label string) (string, bool) {
	trimmed := strings.TrimSpace(label)
	norm := strings.ToLower(trimmed)
	if _, ok := m.labels[norm]; ok {
		return trimmed, true
	}
	for _, p := range m.prefixes {
		// Lowercase (but do not trim) the prefix+delimiter so a meaningful
		// delimiter like ": " is preserved; norm and trimmed share a length, so
		// slicing trimmed by the matched prefix length stays aligned.
		pfx := strings.ToLower(p.Prefix + p.Delimiter)
		if pfx == "" || !strings.HasPrefix(norm, pfx) {
			continue
		}
		if name := strings.TrimSpace(trimmed[len(pfx):]); name != "" {
			return name, true
		}
	}
	return "", false
}

// normalizeLabel folds a label name for case-insensitive matching. GitHub label
// names are case-sensitive at creation but matched case-insensitively, so a
// manifest "deferred" must match an issue's "DEFERRED".
func normalizeLabel(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

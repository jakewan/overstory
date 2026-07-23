package reduce

// AreaClassifier maps an issue's labels onto the repository's functional areas,
// and remembers each area's canonical display name across every issue it has
// seen.
//
// It exists because both composite reads classify areas and a caller correlates
// their output: if the grooming read calls an area "Core" and the orientation
// read calls the same area "core", the two tools describe the same repository
// inconsistently and nothing in either one's tests can see it. Sharing the
// classifier makes that agreement structural rather than a convention two
// packages separately promise to honor.
//
// Accumulation is stateful — Display is only meaningful for keys a prior Keys
// call recorded — so one classifier serves one reduction pass.
type AreaClassifier struct {
	matcher LabelMatcher
	display map[string]string
}

// NewAreaClassifier builds a classifier over a repository's area conventions: an
// explicit label list, prefix rules, or both.
func NewAreaClassifier(labels []string, prefixes []PrefixRule) *AreaClassifier {
	return &AreaClassifier{
		matcher: NewLabelMatcher(labels, prefixes),
		display: make(map[string]string),
	}
}

// Keys returns the distinct area keys present on one issue's labels, recording
// each area's canonical display name as it goes. The set is distinct so an issue
// carrying several labels for one area counts once for that area rather than
// once per label — the difference between "issues in this area" and "labels
// naming this area".
//
// An issue matching no area yields an empty set; callers treat that as
// unclassified rather than as an error.
func (c *AreaClassifier) Keys(labels []string) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, label := range labels {
		name, ok := c.matcher.Match(label)
		if !ok {
			continue
		}
		key := NormalizeLabel(name)
		keys[key] = struct{}{}
		// The canonical form is the lexicographically smallest original seen for the
		// key. Min is order-independent, so the name a repository reports does not
		// depend on the order issues came back from the fetch.
		if cur, seen := c.display[key]; !seen || name < cur {
			c.display[key] = name
		}
	}
	return keys
}

// Display returns an area key's canonical display name, or the empty string for a
// key no Keys call has recorded. It does not fall back to the key itself: the key
// is a normalized form, and presenting it as a display name would show an
// operator a label they never wrote.
func (c *AreaClassifier) Display(key string) string {
	return c.display[key]
}

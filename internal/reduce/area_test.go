package reduce

import (
	"reflect"
	"sort"
	"testing"
)

// TestAreaClassifierCollapsesVariantsToOneKey pins the behavior both area
// reductions depend on: an explicit label and a prefix-matched label naming the
// same area, in any casing, are one area — not two. Before this type existed the
// rule was implemented separately in the grooming and orientation reductions, so
// the two tools could name the same area differently without any test noticing.
func TestAreaClassifierCollapsesVariantsToOneKey(t *testing.T) {
	c := NewAreaClassifier([]string{"Core"}, []PrefixRule{{Prefix: "area", Delimiter: "/"}})

	keys := c.Keys([]string{"Core", "area/core", "unrelated"})
	if len(keys) != 1 {
		t.Fatalf("Keys returned %d areas, want 1: %v", len(keys), keys)
	}
	for k := range keys {
		if got := c.Display(k); got != "Core" {
			t.Errorf("Display(%q) = %q, want Core (lexicographically smallest form seen)", k, got)
		}
	}
}

// TestAreaClassifierDisplayIsOrderIndependent pins that the canonical name does
// not depend on which issue was classified first — accumulation order varies with
// the fetch, so an order-dependent rule would make the same repository report
// different area names between runs.
func TestAreaClassifierDisplayIsOrderIndependent(t *testing.T) {
	forward := NewAreaClassifier(nil, []PrefixRule{{Prefix: "area", Delimiter: "/"}})
	forward.Keys([]string{"area/Zebra"})
	forward.Keys([]string{"area/zebra"})

	reverse := NewAreaClassifier(nil, []PrefixRule{{Prefix: "area", Delimiter: "/"}})
	reverse.Keys([]string{"area/zebra"})
	reverse.Keys([]string{"area/Zebra"})

	key := NormalizeLabel("Zebra")
	if forward.Display(key) != reverse.Display(key) {
		t.Errorf("display depends on classification order: %q vs %q",
			forward.Display(key), reverse.Display(key))
	}
	if got := forward.Display(key); got != "Zebra" {
		t.Errorf("Display = %q, want Zebra", got)
	}
}

func TestAreaClassifierKeysAreDistinctPerIssue(t *testing.T) {
	c := NewAreaClassifier(nil, []PrefixRule{{Prefix: "area", Delimiter: "/"}})

	for _, tc := range []struct {
		name   string
		labels []string
		want   []string
	}{
		{"no area labels", []string{"bug", "p1"}, nil},
		{"one area, repeated forms", []string{"area/core", "area/Core"}, []string{"core"}},
		{"two distinct areas", []string{"area/core", "area/http"}, []string{"core", "http"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for k := range c.Keys(tc.labels) {
				got = append(got, k)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Keys(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

// TestAreaClassifierUnknownKeyDisplay pins the degenerate lookup: a key never
// classified has no canonical form, and returning the key itself would silently
// present a normalized name as if it were an operator's original label.
func TestAreaClassifierUnknownKeyDisplay(t *testing.T) {
	c := NewAreaClassifier(nil, []PrefixRule{{Prefix: "area", Delimiter: "/"}})
	if got := c.Display("never-seen"); got != "" {
		t.Errorf("Display of an unclassified key = %q, want empty", got)
	}
}

package reduce

import "testing"

func TestLabelMatcherExplicitList(t *testing.T) {
	m := NewLabelMatcher([]string{"http", "fs"}, nil)
	// Explicit match returns the issue's original-cased label.
	if name, ok := m.Match("HTTP"); !ok || name != "HTTP" {
		t.Errorf("Match(HTTP) = (%q,%v), want (HTTP,true) — case-insensitive, original casing echoed", name, ok)
	}
	if _, ok := m.Match("buffer"); ok {
		t.Error("Match(buffer) matched, want no match")
	}
}

func TestLabelMatcherPrefixDelimiters(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rule      PrefixRule
		label     string
		wantName  string
		wantMatch bool
	}{
		{"slash", PrefixRule{"area", "/"}, "area/networking", "networking", true},
		{"colon no space", PrefixRule{"area", ":"}, "area:core", "core", true},
		{"colon covers colon-space", PrefixRule{"area", ":"}, "area: core", "core", true},
		{"colon-space delimiter", PrefixRule{"Component", ": "}, "Component: DOM", "DOM", true},
		{"dash", PrefixRule{"area", "-"}, "area-System.Net", "System.Net", true},
		{"case-insensitive prefix, suffix casing kept", PrefixRule{"area", "/"}, "Area/Core", "Core", true},
		{"non-match", PrefixRule{"area", "/"}, "bug", "", false},
		{"empty suffix does not match", PrefixRule{"area", "/"}, "area/", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewLabelMatcher(nil, []PrefixRule{tc.rule})
			name, ok := m.Match(tc.label)
			if ok != tc.wantMatch || name != tc.wantName {
				t.Errorf("Match(%q) = (%q,%v), want (%q,%v)", tc.label, name, ok, tc.wantName, tc.wantMatch)
			}
		})
	}
}

func TestLabelMatcherPrefixLowercaseByteLength(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rule      PrefixRule
		label     string
		wantName  string
		wantMatch bool
	}{
		// strings.ToLower maps runes 1:1 (simple mapping, not full casefold), and
		// several capitals shrink in byte length: the Kelvin sign U+212A -> "k"
		// (3 -> 1 byte) and dotted-capital-I U+0130 -> "i" (2 -> 1). Slicing the
		// original label by the *lowercased* prefix length then cut mid-rune (the
		// pre-fix bug returned "\xaa/networking" and left the delimiter on as
		// "/core"); the fix advances one original rune per prefix rune, so the
		// suffix stays intact and original-cased.
		{"multibyte prefix rune shrinks", PrefixRule{"k", "/"}, "K/networking", "networking", true},
		{"multibyte leading label rune shrinks", PrefixRule{"i", "/"}, "İ/core", "core", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewLabelMatcher(nil, []PrefixRule{tc.rule})
			name, ok := m.Match(tc.label)
			if ok != tc.wantMatch || name != tc.wantName {
				t.Errorf("Match(%q) = (%q,%v), want (%q,%v)", tc.label, name, ok, tc.wantName, tc.wantMatch)
			}
		})
	}
}

func TestLabelMatcherMultiplePrefixes(t *testing.T) {
	m := NewLabelMatcher(nil, []PrefixRule{{"area", "/"}, {"Component", ": "}})
	if name, ok := m.Match("Component: Hooks"); !ok || name != "Hooks" {
		t.Errorf("Match(Component: Hooks) = (%q,%v), want (Hooks,true)", name, ok)
	}
	if name, ok := m.Match("area/api"); !ok || name != "api" {
		t.Errorf("Match(area/api) = (%q,%v), want (api,true)", name, ok)
	}
}

func TestLabelMatcherExplicitTakesPrecedence(t *testing.T) {
	// A label that satisfies both the explicit list and a prefix is named by the
	// list (the full original label), not the prefix suffix.
	m := NewLabelMatcher([]string{"area/core"}, []PrefixRule{{"area", "/"}})
	if name, ok := m.Match("area/core"); !ok || name != "area/core" {
		t.Errorf("Match(area/core) = (%q,%v), want (area/core,true) — list precedence", name, ok)
	}
}

func TestLabelMatcherEmpty(t *testing.T) {
	m := NewLabelMatcher(nil, nil)
	if _, ok := m.Match("anything"); ok {
		t.Error("empty matcher matched, want no match")
	}
}

func TestLabelMatcherMatchesAny(t *testing.T) {
	m := NewLabelMatcher([]string{"bug", "deferred"}, []PrefixRule{{"area", "/"}})
	for _, tc := range []struct {
		name   string
		labels []string
		want   bool
	}{
		{"empty label set", nil, false},
		{"single explicit match", []string{"bug"}, true},
		{"match among several", []string{"docs", "area/core", "wontfix"}, true},
		{"no label matches", []string{"docs", "wontfix"}, false},
		{"whitespace-only never matches", []string{"  ", "\t"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.MatchesAny(tc.labels); got != tc.want {
				t.Errorf("MatchesAny(%q) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

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

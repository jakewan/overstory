package backlog

import "testing"

func TestLabelMatcherExplicitList(t *testing.T) {
	m := newLabelMatcher([]string{"http", "fs"}, nil)
	// Explicit match returns the issue's original-cased label.
	if name, ok := m.match("HTTP"); !ok || name != "HTTP" {
		t.Errorf("match(HTTP) = (%q,%v), want (HTTP,true) — case-insensitive, original casing echoed", name, ok)
	}
	if _, ok := m.match("buffer"); ok {
		t.Error("match(buffer) matched, want no match")
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
			m := newLabelMatcher(nil, []PrefixRule{tc.rule})
			name, ok := m.match(tc.label)
			if ok != tc.wantMatch || name != tc.wantName {
				t.Errorf("match(%q) = (%q,%v), want (%q,%v)", tc.label, name, ok, tc.wantName, tc.wantMatch)
			}
		})
	}
}

func TestLabelMatcherMultiplePrefixes(t *testing.T) {
	m := newLabelMatcher(nil, []PrefixRule{{"area", "/"}, {"Component", ": "}})
	if name, ok := m.match("Component: Hooks"); !ok || name != "Hooks" {
		t.Errorf("match(Component: Hooks) = (%q,%v), want (Hooks,true)", name, ok)
	}
	if name, ok := m.match("area/api"); !ok || name != "api" {
		t.Errorf("match(area/api) = (%q,%v), want (api,true)", name, ok)
	}
}

func TestLabelMatcherExplicitTakesPrecedence(t *testing.T) {
	// A label that satisfies both the explicit list and a prefix is named by the
	// list (the full original label), not the prefix suffix.
	m := newLabelMatcher([]string{"area/core"}, []PrefixRule{{"area", "/"}})
	if name, ok := m.match("area/core"); !ok || name != "area/core" {
		t.Errorf("match(area/core) = (%q,%v), want (area/core,true) — list precedence", name, ok)
	}
}

func TestLabelMatcherEmpty(t *testing.T) {
	m := newLabelMatcher(nil, nil)
	if _, ok := m.match("anything"); ok {
		t.Error("empty matcher matched, want no match")
	}
}

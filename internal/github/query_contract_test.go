package github

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// The GraphQL queries are raw string constants and the wire-decode structs are
// separate: a field requested in one but renamed or mistyped in the other
// compiles cleanly and silently zero-values at runtime. These tests are the
// compile-adjacent guard against that drift — they assert every json field a
// decode struct reads is actually requested in its query. They cover the
// internal struct↔query direction; the struct↔GitHub-schema direction (a field
// our query names that the real API lacks) is the gated live test's job, since
// only a real call can see GitHub's schema.

var identifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// queryIdentifiers is the set of identifier tokens in a GraphQL query, so a field
// is matched as a whole word — "id" must not be satisfied by "isCrossRepository".
func queryIdentifiers(query string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range identifierPattern.FindAllString(query, -1) {
		out[tok] = true
	}
	return out
}

// missingQueryFields returns the json field names reachable in sample's type that
// the query never requests, sorted for stable assertions. It walks structs,
// pointers, and slices recursively; time.Time is a leaf (its fields are not wire
// fields of ours). It is the guard both the real-query assertions and the drift
// self-test exercise.
func missingQueryFields(query string, sample any) []string {
	idents := queryIdentifiers(query)
	want := map[string]struct{}{}
	collectJSONFields(reflect.TypeOf(sample), want)

	var missing []string
	for name := range want {
		if !idents[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func collectJSONFields(t reflect.Type, out map[string]struct{}) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name != "" {
			out[name] = struct{}{}
		}
		// Descend even when this field is untagged, so an anonymous wrapper struct
		// doesn't hide its tagged children.
		collectJSONFields(f.Type, out)
	}
}

// TestQueryDecodeContract pins that every fetch query requests every field its
// decode structs read. Checking the connection structs transitively covers the
// node structs they nest; rateLimitNode is checked separately because it decodes
// from the query root, not from repository.issues.
func TestQueryDecodeContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		sample any
	}{
		{"open-issue connection", issuesQuery, issuesConnection{}},
		{"open-issue rate limit", issuesQuery, rateLimitNode{}},
		{"activity connection", activityQuery, activityConnection{}},
		{"activity rate limit", activityQuery, rateLimitNode{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if missing := missingQueryFields(tc.query, tc.sample); len(missing) > 0 {
				t.Errorf("query does not request decoded fields %v — struct and query have drifted", missing)
			}
		})
	}
}

// TestMissingQueryFieldsCatchesDrift proves the guard is not vacuous: a query that
// drops a field the struct reads must be flagged, and a complete one must not.
func TestMissingQueryFieldsCatchesDrift(t *testing.T) {
	// closedAt dropped — the exact kind of typo the live smoke used to be the only
	// thing catching.
	incomplete := `issues{ nodes{ number createdAt updatedAt } }`
	missing := missingQueryFields(incomplete, issueActivityNode{})
	if len(missing) != 1 || missing[0] != "closedAt" {
		t.Errorf("missing = %v, want [closedAt] (the guard must catch a dropped field)", missing)
	}

	complete := `issues{ nodes{ number createdAt closedAt updatedAt } }`
	if missing := missingQueryFields(complete, issueActivityNode{}); len(missing) != 0 {
		t.Errorf("missing = %v, want none for a complete query", missing)
	}
}

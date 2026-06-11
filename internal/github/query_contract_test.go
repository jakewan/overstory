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

var (
	identifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	// argListPattern matches a parenthesized argument list or the operation
	// signature. Neither nests parentheses in these queries (inner structure uses
	// {} and []), so a single non-greedy-equivalent character-class match is exact.
	argListPattern = regexp.MustCompile(`\([^)]*\)`)
)

// queryIdentifiers is the set of identifier tokens in a GraphQL query's selection
// sets, so a field is matched as a whole word — "id" must not be satisfied by
// "isCrossRepository". Argument lists and the operation signature are stripped
// first: an argument or variable identifier (e.g. the `name` in
// `repository(owner:$owner, name:$name)`) must not masquerade as a selected field
// and mask the drift of a same-named selection (the label node's `name`).
func queryIdentifiers(query string) map[string]bool {
	selections := argListPattern.ReplaceAllString(query, "")
	out := map[string]bool{}
	for _, tok := range identifierPattern.FindAllString(selections, -1) {
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
//
// When you add a query or a decode struct, add it to the cases below — the guard
// only inspects the structs these cases enumerate, so a new decode type is
// otherwise silently unguarded (the drift this test exists to catch).
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

	// A field whose name appears only inside an argument list (here `name:$name`),
	// never as a selection, must still read as missing — argument identifiers are
	// stripped before matching, so they can't mask a dropped selection.
	argOnly := `repository(owner:$owner, name:$name){ issues{ nodes{ number } } }`
	sample := struct {
		Name string `json:"name"`
	}{}
	if missing := missingQueryFields(argOnly, sample); len(missing) != 1 || missing[0] != "name" {
		t.Errorf("missing = %v, want [name] (an argument identifier must not mask a dropped selection)", missing)
	}
}

// TestQueryStructuralKeys covers the wrapper keys the decode path unmarshals
// through but the field-level contract test does not reach: the response is
// decoded via repository -> issues and a root rateLimit, and renaming any of those
// selections would compile and silently zero-value. These keys are a small fixed
// set (not a growing field list), so an explicit check is the apt shape.
func TestQueryStructuralKeys(t *testing.T) {
	for _, q := range []struct{ name, query string }{
		{"open issues", issuesQuery},
		{"activity", activityQuery},
	} {
		t.Run(q.name, func(t *testing.T) {
			idents := queryIdentifiers(q.query)
			for _, key := range []string{"repository", "issues", "rateLimit"} {
				if !idents[key] {
					t.Errorf("query does not select structural key %q the decode path depends on", key)
				}
			}
		})
	}
}

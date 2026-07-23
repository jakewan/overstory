package github

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// SCAFFOLDING — delete once the page-size extraction has landed and its own
// tests pass.
//
// The page-size extraction rewrites issuesQuery and criticalPathIssuesQuery from
// const literals into strings built at init, and the change must be byte-neutral:
// only the *source* of each page size moves, never the query GitHub receives.
// Nothing already in this package can check that. TestQueryDecodeContract and
// TestQueryStructuralKeys both run queryIdentifiers, which strips every
// parenthesized argument list before matching — and page sizes exist only inside
// argument lists, so a transposed Sprintf argument yielding labels(first:50)
// passes both. These digests close that window for the duration of the change.
//
// This is deliberately temporary. A permanent golden would be a verbatim second
// copy of each query that every legitimate query edit has to update — the same
// restated-fact problem the extraction exists to remove, relocated into a test.
const (
	goldenIssuesQuery             = "c4599e2da23effc656d588a14cd3ad0ad6c74a24f15d9cc6f33179696be6a30b"
	goldenCriticalPathIssuesQuery = "e7104b62667410ba2b0919b967eab185ef75bbab957bf77d31c0a96736fe3e1b"
)

func TestQueryBytesUnchangedByExtraction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"issuesQuery", issuesQuery, goldenIssuesQuery},
		{"criticalPathIssuesQuery", criticalPathIssuesQuery, goldenCriticalPathIssuesQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sum := sha256.Sum256([]byte(tc.query))
			if got := hex.EncodeToString(sum[:]); got != tc.want {
				t.Errorf("%s digest = %s, want %s\nquery is now:\n%s", tc.name, got, tc.want, tc.query)
			}
		})
	}
}

package reduce

import (
	"testing"

	"github.com/jakewan/overstory/internal/github"
)

func TestOpenDependencyNumbers(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []github.DependencyRef
		want []int
	}{
		{
			name: "open only, sorted ascending",
			refs: []github.DependencyRef{{Number: 11, Open: true}, {Number: 7, Open: true}},
			want: []int{7, 11},
		},
		{
			name: "closed edges dropped",
			refs: []github.DependencyRef{{Number: 7, Open: true}, {Number: 9, Open: false}},
			want: []int{7},
		},
		{
			name: "duplicates deduped",
			refs: []github.DependencyRef{{Number: 7, Open: true}, {Number: 7, Open: true}},
			want: []int{7},
		},
		{
			name: "all closed yields empty",
			refs: []github.DependencyRef{{Number: 7, Open: false}},
			want: []int{},
		},
		{
			name: "nil input yields empty",
			refs: nil,
			want: []int{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := OpenDependencyNumbers(tc.refs)
			// Non-nil is part of the contract (serializes [] not null), so assert it
			// explicitly rather than letting an empty-vs-nil equality pass silently.
			if got == nil {
				t.Fatal("OpenDependencyNumbers returned nil, want non-nil empty slice")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("OpenDependencyNumbers = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("OpenDependencyNumbers = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

// TestOpenExternalDependencies is the cross-repository counterpart, and pins the two
// properties the bare-number projection gets for free but this one does not: a
// reference is distinct per repository *and* number, and the order is repo-then-number
// so two repositories' blockers do not interleave by number alone.
func TestOpenExternalDependencies(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []github.ExternalDependencyRef
		want []ExternalRef
	}{
		{
			name: "open only, ordered by repository then number",
			refs: []github.ExternalDependencyRef{
				{Repo: "acme/gadgets", Number: 12, Open: true},
				{Repo: "acme/doodads", Number: 3, Open: true},
				{Repo: "acme/gadgets", Number: 4, Open: true},
			},
			want: []ExternalRef{
				{Repo: "acme/doodads", Number: 3},
				{Repo: "acme/gadgets", Number: 4},
				{Repo: "acme/gadgets", Number: 12},
			},
		},
		{
			// The same number in two repositories is two blockers, not a duplicate —
			// the distinction the bare-number list cannot make, and the reason this
			// field exists.
			name: "the same number in two repositories is two references",
			refs: []github.ExternalDependencyRef{
				{Repo: "acme/gadgets", Number: 7, Open: true},
				{Repo: "other/repo", Number: 7, Open: true},
				{Repo: "acme/gadgets", Number: 7, Open: true},
			},
			want: []ExternalRef{
				{Repo: "acme/gadgets", Number: 7},
				{Repo: "other/repo", Number: 7},
			},
		},
		{
			name: "closed blockers are dropped",
			refs: []github.ExternalDependencyRef{{Repo: "other/repo", Number: 7, Open: false}},
			want: []ExternalRef{},
		},
		{
			name: "nil input yields empty",
			refs: nil,
			want: []ExternalRef{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := OpenExternalDependencies(tc.refs)
			if got == nil {
				t.Fatal("OpenExternalDependencies returned nil, want non-nil empty slice")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("OpenExternalDependencies = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("OpenExternalDependencies = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

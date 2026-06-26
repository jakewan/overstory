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

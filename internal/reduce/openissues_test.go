package reduce

import "testing"

func TestNewOpenIssueSet(t *testing.T) {
	for _, tc := range []struct {
		name      string
		numbers   []int
		truncated bool
		want      []int
	}{
		{"empty is non-nil", nil, false, []int{}},
		{"sorts ascending", []int{30, 10, 20}, false, []int{10, 20, 30}},
		{"dedupes", []int{10, 20, 10, 20, 30}, false, []int{10, 20, 30}},
		{"sorts and dedupes together", []int{5, 1, 5, 3, 1}, false, []int{1, 3, 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewOpenIssueSet(tc.numbers, tc.truncated)
			if got.Numbers == nil {
				t.Fatal("Numbers = nil, want non-nil (serializes [] not null)")
			}
			if len(got.Numbers) != len(tc.want) {
				t.Fatalf("Numbers = %v, want %v", got.Numbers, tc.want)
			}
			for i, w := range tc.want {
				if got.Numbers[i] != w {
					t.Errorf("Numbers[%d] = %d, want %d (full: %v)", i, got.Numbers[i], w, got.Numbers)
				}
			}
			if got.FetchTruncated != tc.truncated {
				t.Errorf("FetchTruncated = %v, want %v", got.FetchTruncated, tc.truncated)
			}
		})
	}
}

// TestNewOpenIssueSetCarriesTruncation pins that the window-coverage flag passes
// through unchanged — the soundness signal a caller reads to know Numbers is a floor.
func TestNewOpenIssueSetCarriesTruncation(t *testing.T) {
	if got := NewOpenIssueSet([]int{1}, true); !got.FetchTruncated {
		t.Error("FetchTruncated = false, want true")
	}
}

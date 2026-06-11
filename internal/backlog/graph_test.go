package backlog

import (
	"reflect"
	"testing"
)

func TestConnectedComponentsTransitive(t *testing.T) {
	// A~B and B~C but A≁C still form one component — the transitivity that lets
	// chained near-duplicates cluster.
	got := connectedComponents(4, [][2]int{{0, 1}, {1, 2}})
	want := [][]int{{0, 1, 2}} // node 3 is a singleton, excluded
	if !reflect.DeepEqual(got, want) {
		t.Errorf("connectedComponents = %v, want %v", got, want)
	}
}

func TestConnectedComponentsDeterministicOrder(t *testing.T) {
	// Components returned in ascending first-member order regardless of edge order.
	got := connectedComponents(5, [][2]int{{3, 4}, {0, 1}})
	want := [][]int{{0, 1}, {3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("connectedComponents = %v, want %v", got, want)
	}
}

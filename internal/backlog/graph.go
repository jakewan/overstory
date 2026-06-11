package backlog

// This file holds the reduction-agnostic graph spine: turn a set of undirected
// edges over node indices into deterministic clusters. Both the title-overlap and
// cross-reference reductions build their own edges (by title similarity, by
// GitHub cross-reference) and share this clustering, so the union-find lives here
// rather than inside either reduction.

// connectedComponents groups node indices [0,n) into their connected components
// under the given undirected edges, returning only components of size >= 2.
// Assembly iterates nodes by ascending index — never by map order — so component
// membership and the order components are returned in are deterministic.
func connectedComponents(n int, edges [][2]int) [][]int {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	for _, e := range edges {
		ra, rb := find(parent, e[0]), find(parent, e[1])
		if ra != rb {
			parent[ra] = rb
		}
	}

	members := make(map[int][]int)
	var rootsInOrder []int
	for i := 0; i < n; i++ {
		r := find(parent, i)
		if _, seen := members[r]; !seen {
			rootsInOrder = append(rootsInOrder, r)
		}
		members[r] = append(members[r], i)
	}
	var out [][]int
	for _, r := range rootsInOrder {
		if len(members[r]) >= 2 {
			out = append(out, members[r])
		}
	}
	return out
}

// find returns the representative root of x in the union-find forest, compressing
// the path it walks (path halving) so repeated lookups stay near-flat.
func find(parent []int, x int) int {
	for parent[x] != x {
		parent[x] = parent[parent[x]]
		x = parent[x]
	}
	return x
}

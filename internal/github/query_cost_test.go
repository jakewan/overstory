package github

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// GitHub scores a GraphQL query's node count before running it and rejects one
// that scores too high. The cost of a connection is its page size multiplied by
// every enclosing connection's page size, so nesting compounds: the budget is a
// property of the query's *shape*, not of its page sizes alone.
//
// That is why this computes the cost from the query text rather than summing the
// page-size constants. A sum is blind to depth: moving one connection inside
// another leaves every constant untouched while the real cost multiplies, so it
// would report headroom that does not exist.
//
// nodeCostBudget is set for early warning rather than cliff-avoidance. GitHub's
// documented ceiling is far above where these queries sit, so a budget at the
// ceiling would stay green until the query were many times heavier — by which
// point the points cost of every call has already grown. This fires when the
// query grows materially, which is the moment worth a second look. The tests log
// each query's current score rather than pinning it here. If GitHub
// lowers the real ceiling below this figure the test still passes and the API
// starts rejecting calls — the failure is loud and immediate at the fetch, not
// silent, so the budget's staleness degrades to a worse error message rather than
// to wrong data.
const nodeCostBudget = 60_000

// pagedQueries is every query that pages a connection, so a guard here covers the
// family rather than the two that happened to be edited. $first is bound to
// pageSize at each call site and supplied here: the outermost multiplier is a
// GraphQL variable and is not in the query text.
func pagedQueries() []struct {
	name  string
	query string
	vars  map[string]int
} {
	v := map[string]int{"first": pageSize}
	return []struct {
		name  string
		query string
		vars  map[string]int
	}{
		{"issuesQuery", issuesQuery, v},
		{"activityQuery", activityQuery, v},
		{"milestonesQuery", milestonesQuery, v},
		{"pullRequestsQuery", pullRequestsQuery, v},
		{"prActivityQuery", prActivityQuery, v},
		{"criticalPathIssuesQuery", criticalPathIssuesQuery, v},
	}
}

func TestPagedQueriesStayUnderNodeCostBudget(t *testing.T) {
	for _, tc := range pagedQueries() {
		t.Run(tc.name, func(t *testing.T) {
			cost, err := queryNodeCost(tc.query, tc.vars)
			if err != nil {
				t.Fatalf("computing node cost: %v", err)
			}
			t.Logf("%s scores %d nodes (budget %d)", tc.name, cost, nodeCostBudget)
			if cost > nodeCostBudget {
				t.Errorf("%s scores %d nodes, over the %d budget — a page size grew or a "+
					"connection was nested inside another; re-check the headroom against "+
					"GitHub's ceiling before raising the budget", tc.name, cost, nodeCostBudget)
			}
		})
	}
}

// TestConnectionPageSizesAreWithinGitHubsCap guards the constraint that actually
// bites when someone retunes a page size — which naming the constants made easier
// to do. GitHub rejects first:/last: outside 1..100 with an argument-validation
// error, so an over-large value fails every live call against every repository
// while the node-cost budget stays green (250 labels still scores well under it)
// and the contract tests stay green (they strip argument lists before matching).
// Nothing else in the package can see it.
func TestConnectionPageSizesAreWithinGitHubsCap(t *testing.T) {
	const githubMaxPageSize = 100
	for _, tc := range pagedQueries() {
		t.Run(tc.name, func(t *testing.T) {
			for _, m := range pageSizeArg.FindAllStringSubmatch(tc.query, -1) {
				val := m[1]
				if strings.HasPrefix(val, "$") {
					continue // a variable; its value is the caller's pageSize, checked below
				}
				n, err := strconv.Atoi(val)
				if err != nil {
					t.Fatalf("page size %q is not a number", val)
				}
				if n < 1 || n > githubMaxPageSize {
					t.Errorf("page size %d is outside GitHub's 1..%d cap — the API rejects "+
						"the whole query, so every fetch using it fails", n, githubMaxPageSize)
				}
			}
		})
	}
	if pageSize < 1 || pageSize > githubMaxPageSize {
		t.Errorf("pageSize = %d is outside GitHub's 1..%d cap", pageSize, githubMaxPageSize)
	}
}

// TestBuiltQueriesHaveNoFormatErrors guards the hazard the extraction introduced:
// the two interpolated queries are Sprintf format strings now, so a later edit
// adding a literal `%` to the query body turns it into a verb and ships
// `%!x(MISSING)` inside the query. The contract tests only ever *add* tokens to an
// identifier set, so garbage never fails them; GitHub reports it as a syntax error
// at fetch time.
func TestBuiltQueriesHaveNoFormatErrors(t *testing.T) {
	for _, tc := range pagedQueries() {
		if strings.Contains(tc.query, "%!") {
			t.Errorf("%s contains a Sprintf format error: %q", tc.name,
				tc.query[max(0, strings.Index(tc.query, "%!")-40):])
		}
	}
}

// TestQueryNodeCostSeesNesting is the guard on the guard: it pins that the cost
// walk multiplies through depth rather than summing siblings. Without this, a
// sibling-sum implementation passes every assertion above and silently stops
// detecting the failure mode the budget exists for.
func TestQueryNodeCostSeesNesting(t *testing.T) {
	siblings := `query{ repository{ issues(first:$first){ nodes{ a(first:50){ x } b(first:25){ y } } } } }`
	nested := `query{ repository{ issues(first:$first){ nodes{ a(first:50){ b(first:25){ y } } } } } }`
	vars := map[string]int{"first": 100}

	sibCost, err := queryNodeCost(siblings, vars)
	if err != nil {
		t.Fatalf("siblings: %v", err)
	}
	nestCost, err := queryNodeCost(nested, vars)
	if err != nil {
		t.Fatalf("nested: %v", err)
	}

	if wantSib := 100 + 100*50 + 100*25; sibCost != wantSib {
		t.Errorf("sibling cost = %d, want %d", sibCost, wantSib)
	}
	if wantNest := 100 + 100*50 + 100*50*25; nestCost != wantNest {
		t.Errorf("nested cost = %d, want %d", nestCost, wantNest)
	}
	if nestCost <= sibCost {
		t.Errorf("nesting did not raise the cost (%d vs %d) — the walk is summing "+
			"siblings rather than multiplying through depth", nestCost, sibCost)
	}
}

// queryNodeCost walks a GraphQL query and returns its node count. Each connection
// contributes its page size times the product of the page sizes enclosing it.
// vars resolves a page size given as a GraphQL variable (`first:$first`) to the
// value bound at the call site; an unresolvable variable is an error rather than
// a silent zero, since a dropped multiplier understates the cost.
//
// It relies on these queries never nesting parentheses inside an argument list —
// the same property queryIdentifiers in the contract test depends on. Inner
// structure uses {} and [], so scanning to the next ')' finds the right one.
func queryNodeCost(query string, vars map[string]int) (int, error) {
	stack := []int{1} // product of enclosing page sizes; the root multiplier is 1
	pending := 0      // page size from the argument list awaiting its '{'
	cost := 0

	for i := 0; i < len(query); i++ {
		switch query[i] {
		case '(':
			end := strings.IndexByte(query[i:], ')')
			if end < 0 {
				return 0, fmt.Errorf("unterminated argument list at offset %d", i)
			}
			size, err := argPageSize(query[i:i+end], vars)
			if err != nil {
				return 0, err
			}
			pending = size
			i += end
		case '{':
			mult := stack[len(stack)-1]
			next := mult
			if pending > 0 {
				cost += pending * mult
				next = mult * pending
			}
			stack = append(stack, next)
			pending = 0
		case '}':
			if len(stack) == 1 {
				return 0, fmt.Errorf("unbalanced closing brace at offset %d", i)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 1 {
		return 0, fmt.Errorf("unbalanced braces: %d unclosed", len(stack)-1)
	}
	return cost, nil
}

// argPageSize returns the page size declared in a GraphQL argument list, or 0
// when the list declares none. A variable reference resolves through vars; an
// unbound variable is an error rather than a silent zero, because a dropped
// multiplier understates the cost and would make the budget look satisfied.
func argPageSize(args string, vars map[string]int) (int, error) {
	m := pageSizeArg.FindStringSubmatch(args)
	if m == nil {
		return 0, nil
	}
	val := m[1]
	if name, ok := strings.CutPrefix(val, "$"); ok {
		n, bound := vars[name]
		if !bound {
			return 0, fmt.Errorf("page size $%s in %q is not bound in vars", name, args)
		}
		return n, nil
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("page size %q in %q is not a number", val, args)
	}
	return n, nil
}

// pageSizeArg matches a first:/last: *argument* and captures its value, which is
// either a literal or a $variable. The leading class excludes a `$` so the
// operation signature's variable declarations (`$first:Int!`) do not match — they
// name a type, they do not pass a page size — and excludes identifier characters
// so a longer argument name ending in "first" cannot match either.
var pageSizeArg = regexp.MustCompile(`(?:^|[^$A-Za-z0-9_])(?:first|last)\s*:\s*(\$?[A-Za-z0-9_]+)`)

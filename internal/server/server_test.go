package server

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect stands up the server over an in-memory transport and returns a
// connected client session, registering cleanup for both ends. It exists
// because every behavior test needs a live client/server pair; the wiring is
// identical across them.
func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() {
		if cerr := serverSession.Close(); cerr != nil {
			t.Errorf("server session close: %v", cerr)
		}
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		if cerr := clientSession.Close(); cerr != nil {
			t.Errorf("client session close: %v", cerr)
		}
	})
	return clientSession
}

// TestServerExposesTools pins the tool contract: the server constructs,
// completes the MCP initialize handshake over a real client/server session, and
// registers exactly the backlog_review, project_summary, milestone_tracks,
// authored_activity, authored_activity_batch, maintenance_activity, and
// maintenance_activity_batch tools. It is the end-to-end wiring proof; each tool's
// behavior is covered in its own _test.go.
func TestServerExposesTools(t *testing.T) {
	ctx := context.Background()
	cs := connect(t, New())

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	got := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"backlog_review", "project_summary", "milestone_tracks", "authored_activity", "authored_activity_batch", "maintenance_activity", "maintenance_activity_batch"} {
		if !got[want] {
			t.Errorf("tool %q not registered; got %v", want, got)
		}
	}
	if len(res.Tools) != 7 {
		t.Errorf("ListTools returned %d tools, want 7", len(res.Tools))
	}
}

// claudeCodeDisplayLimit is how much of a tool description, and of the server
// instructions, Claude Code shows a model: text past it is cut and marked
// "… [truncated]". The limit is undocumented. It was read from Claude Code's own
// truncation code and is reported in anthropics/claude-code#87650. If it is lifted,
// the tests holding text to it only leave room unused; if it is lowered, text these
// tests pass will be cut again without any signal here.
const claudeCodeDisplayLimit = 2048

// claudeCodeLen measures text the way Claude Code applies claudeCodeDisplayLimit: as a
// JavaScript string length, in UTF-16 code units. A character outside the Basic
// Multilingual Plane counts twice, so a rune count would pass text Claude Code cuts.
func claudeCodeLen(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// TestServerInstructionsStateDependencyReductions pins what the server tells a caller
// about the dependency fields backlog_review and project_summary project. Those fields
// are reductions rather than GitHub's own lists: told nothing, a caller describes the
// filtering in its own words, or reads the sub-issue gap as an exact count of open
// children. The statement lives in the server instructions because it is shared by
// both tools and is sent once per session, leaving each description's budget for what
// that tool alone needs.
func TestServerInstructionsStateDependencyReductions(t *testing.T) {
	cs := connect(t, New())
	instructions := cs.InitializeResult().Instructions

	// One token per property, so a rewording that keeps the meaning keeps passing and
	// dropping any single property fails.
	for _, want := range []string{
		// which edges survive, and in what shape
		"not GitHub's lists",
		"same-repository",
		"still open",
		"ascending",
		"distinct",
		"moves to blockedByExternal",
		"cross-repository blocking and sub-issue edges are dropped",
		"ordered by repository then number",
		// how each list is capped
		"blockedByTruncated",
		"blockingTruncated",
		"subIssuesTruncated",
		"blockedByTruncated bounds blockedByExternal too",
		// what the sub-issue gap is and is not
		"upper bound on open children",
		"not an exact count",
		"over-report",
		"not a measure of how many children subIssues omits",
		// the derived fields, each with the tool that projects it
		"subIssueGate",
		"gatesPrioritized",
		"blockingCount",
		"may not be their only blocker",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("server instructions lack %q", want)
		}
	}

	// Claude Code cuts the instructions at the same limit as a tool description.
	if n := claudeCodeLen(instructions); n > claudeCodeDisplayLimit {
		t.Errorf("server instructions run %d characters, want at most %d", n, claudeCodeDisplayLimit)
	}
}

// TestToolDescriptionsFitClaudeCodeDisplayLimit holds every registered tool's
// description to what Claude Code shows a model. A description past the limit is cut
// with no warning to its author, and the part cut is the contract a model reads before
// it calls the tool. Ranging over ListTools covers a tool added later without edits.
func TestToolDescriptionsFitClaudeCodeDisplayLimit(t *testing.T) {
	cs := connect(t, New())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range res.Tools {
		if n := claudeCodeLen(tool.Description); n > claudeCodeDisplayLimit {
			t.Errorf("%s description runs %d characters, want at most %d", tool.Name, n, claudeCodeDisplayLimit)
		}
	}
}

// readinessDescriptions returns the backlog_review and project_summary descriptions by
// tool name, failing the test if either is missing.
func readinessDescriptions(t *testing.T) map[string]string {
	t.Helper()
	cs := connect(t, New())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	descs := make(map[string]string, 2)
	for _, tool := range res.Tools {
		if tool.Name == "backlog_review" || tool.Name == "project_summary" {
			descs[tool.Name] = tool.Description
		}
	}
	if len(descs) != 2 {
		t.Fatalf("found %d readiness tools, want 2", len(descs))
	}
	return descs
}

// TestReadinessToolDescriptionsDescribeEveryBlock pins that each readiness description
// gives every projectable block, and the always-returned openIssueSet, a clause of its
// own. The blocks enum already lists the names; what it cannot say is what each block
// means. A block is matched as a clause key, its name followed by " (", because a bare
// name also turns up in prose ("deferred labels") and would pass with no clause at all.
func TestReadinessToolDescriptionsDescribeEveryBlock(t *testing.T) {
	descs := readinessDescriptions(t)
	for tool, names := range map[string][]string{
		"backlog_review":  backlogBlockNames,
		"project_summary": summaryBlockNames,
	} {
		for _, name := range append(slices.Clone(names), "openIssueSet") {
			if !strings.Contains(descs[tool], name+" (") {
				t.Errorf("%s description has no %q clause", tool, name)
			}
		}
	}
}

// TestReadinessToolDescriptionsStateRenderTimeRules pins the rules a model needs to read
// each readiness response correctly, and which it can learn only from the description.
// One token per rule, so a rewording that keeps the meaning keeps passing and dropping
// any single rule fails.
func TestReadinessToolDescriptionsStateRenderTimeRules(t *testing.T) {
	shared := []string{
		// an issue missing from openIssueSet is not thereby resolved
		"not proof of resolution",
		// readiness is one verdict, counted the same way in both places
		"same verdict the dependencies block counts",
		// what provisional means, and its two causes
		"truncated edge list",
		"unread seam",
		"empty edge list is not evidence of readiness",
		// the critical-path gate's own, distinct provisional state
		"provisional under a truncated fetch",
		// truncation and degradation
		"flags mark a floor",
		"available:false",
		"sizeBound",
	}
	descs := readinessDescriptions(t)
	for tool, own := range map[string][]string{
		"backlog_review": {
			// deferred issues are parked, not neglected
			"deferred ones excluded",
			// native edges outrank the mention graph's direction
			"crossRef mention graph can invert",
		},
		"project_summary": {
			// a missing-area count with no area labels seen is not a defect list
			"observation to investigate",
			// a capped label list can hide an area or deferred label either way
			"either direction",
			"flagged when members are a floor",
			"ranking stays with the caller",
		},
	} {
		for _, want := range append(slices.Clone(shared), own...) {
			if !strings.Contains(descs[tool], want) {
				t.Errorf("%s description lacks %q", tool, want)
			}
		}
	}
}

// TestReadinessToolDescriptionsDoNotAttributeReductionsToGitHub pins the other half of
// the same contract: the open filter and the same-repository split are the server's,
// so neither readiness tool's description may present the reduced lists as GitHub's.
func TestReadinessToolDescriptionsDoNotAttributeReductionsToGitHub(t *testing.T) {
	cs := connect(t, New())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	checked := 0
	for _, tool := range res.Tools {
		if tool.Name != "backlog_review" && tool.Name != "project_summary" {
			continue
		}
		checked++
		for _, attribution := range []string{"authoritative native", "GitHub's authoritative"} {
			if strings.Contains(tool.Description, attribution) {
				t.Errorf("%s description calls the reduced edges %q", tool.Name, attribution)
			}
		}
	}
	if checked != 2 {
		t.Errorf("checked %d readiness tools, want 2", checked)
	}
}

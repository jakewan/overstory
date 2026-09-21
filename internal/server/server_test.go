package server

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestServerInstructionsStateDependencyReductions pins what the server tells a caller
// about the dependency fields backlog_review and project_summary project. Those fields
// are reductions rather than GitHub's own lists: told nothing, a caller describes the
// filtering in its own words, or reads the sub-issue gap as an exact count of open
// children. The statement lives in the server instructions rather than either tool
// description because Claude Code shows a model only the first 2,048 characters of a
// description, and both readiness descriptions run past that.
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

	// The instructions' own display limit in Claude Code is undocumented. Holding them
	// to the description cap, the one limit observed, keeps the statement deliverable
	// if the two turn out to share it.
	if n := utf8.RuneCountInString(instructions); n > 2048 {
		t.Errorf("server instructions run %d characters, want at most 2048", n)
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

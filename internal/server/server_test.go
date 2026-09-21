package server

import (
	"context"
	"strings"
	"testing"

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

// TestToolDescriptionsStateDependencyReductions pins what the two readiness tools tell
// a caller about their dependency fields. The description is the one channel that
// reaches the rendering model, and the fields are reductions rather than GitHub's own
// lists: without these statements a caller describes the filtering in its own words,
// or reads the sub-issue gap as an exact count of open children.
func TestToolDescriptionsStateDependencyReductions(t *testing.T) {
	ctx := context.Background()
	cs := connect(t, New())

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	descriptions := make(map[string]string, len(res.Tools))
	for _, tool := range res.Tools {
		descriptions[tool.Name] = tool.Description
	}

	// The shared statement: which edges survive, in what order, how each list is
	// capped, and what the gap bounds.
	shared := []string{
		"reduced server-side",
		"blockedBy, blocking, and subIssues are the ascending, distinct numbers of same-repository issues still open",
		"blockedByTruncated",
		"blockingTruncated",
		"subIssuesTruncated",
		"blockedByTruncated bounds it too",
		"upper bound on open children",
		"over-report",
	}
	cases := []struct {
		tool    string
		derived []string
	}{
		{tool: "backlog_review", derived: []string{"subIssueGate"}},
		{tool: "project_summary", derived: []string{"gatesPrioritized", "blockingCount"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			desc, ok := descriptions[tc.tool]
			if !ok {
				t.Fatalf("tool %q not registered", tc.tool)
			}
			for _, want := range append(append([]string{}, shared...), tc.derived...) {
				if !strings.Contains(desc, want) {
					t.Errorf("description lacks %q", want)
				}
			}
			// The open filter and the same-repository split are the server's, so the
			// description must not attribute the lists to GitHub.
			if strings.Contains(desc, "authoritative native") {
				t.Errorf("description still calls the reduced edges %q", "authoritative native")
			}
		})
	}
}

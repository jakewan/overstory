package server

import (
	"context"
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

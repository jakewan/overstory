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

// TestServerExposesNoToolsYet pins the bootstrap contract: the server
// constructs, completes the MCP initialize handshake over a real client/server
// session, and registers no tools. It is the end-to-end wiring proof and the
// anchor the first real tool's test will extend — when backlog_review lands,
// this expectation changes with it.
func TestServerExposesNoToolsYet(t *testing.T) {
	ctx := context.Background()
	cs := connect(t, New())

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) != 0 {
		t.Errorf("ListTools returned %d tools, want 0", len(res.Tools))
	}
}

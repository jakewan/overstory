// Command overstory is a manifest-driven GitHub project-management MCP server.
// It surveys a repository's issue and PR landscape from above — hot spots,
// stale pockets, whole-project trends — and returns compact structured facts
// for the caller to render, applying each repository's conventions from a
// declarative manifest rather than from hardcoded constants.
//
// This is the bootstrap entrypoint: it constructs the server — which registers
// the tools and resolves each repository's manifest conventions — and speaks
// MCP over stdio. What ships here is the runnable shell: construct, serve, and
// classify shutdown so a client disconnect ends the process cleanly rather than
// as a failure.
package main

import (
	"context"
	"errors"
	"io"
	"log"

	"github.com/jakewan/overstory/internal/server"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// codeServerClosing is the SDK's JSON-RPC error code for a connection torn down
// because the server is shutting down. It is not among the jsonrpc package's
// exported standard codes, so it is named here. Matching the code rather than
// the message text keeps clean-shutdown detection stable across SDK upgrades
// that reword the message.
const codeServerClosing = -32004

// run serves a session over the given transport, returning nil when the session
// ends normally (the client disconnects) and an error only on a genuine
// failure. It is factored out of main so the shutdown classification is
// testable over an in-process transport.
func run(ctx context.Context, srv *mcp.Server, transport mcp.Transport) error {
	if err := srv.Run(ctx, transport); err != nil && !isCleanShutdown(err) {
		return err
	}
	return nil
}

// isCleanShutdown reports whether err is the routine end of a session rather
// than a failure: a nil error, the input stream reaching EOF, or the SDK's
// server-closing signal raised as in-flight calls are torn down on disconnect.
func isCleanShutdown(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == codeServerClosing
}

func main() {
	ctx := context.Background()
	// main is the one place a fatal is acceptable: a failed serve has nowhere
	// left to return to. Diagnostics go to stderr via log — stdout carries the
	// JSON-RPC protocol stream and nothing else.
	if err := run(ctx, server.New(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp server: %v", err)
	}
}

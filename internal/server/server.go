// Package server builds the overstory MCP server: a manifest-driven,
// project-management server that reduces a repository's issue and PR landscape
// to compact structured facts and leaves rendering to the caller.
//
// The split of responsibility is deliberate and load-bearing: this server is
// pure mechanism — it fetches, computes, and reduces. Deciding how to present
// the result, and which narrative to wrap it in, is the calling agent's job.
package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "overstory"
	serverVersion = "0.1.0"
)

// New builds the overstory MCP server. It exposes no tools yet: the
// backlog_review tool and the manifest resolution behind it arrive in their
// own changes. When tools are added, register them with mcp.AddTool, publish
// their input constraints (defaults, bounds, required fields) in the JSON
// schema rather than in handler code, and guard a literal-null arguments
// payload with a receiving middleware so schema defaults apply cleanly.
func New() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
}

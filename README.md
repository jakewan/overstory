# Overstory

A generic, manifest-driven GitHub project-management MCP server.

Overstory surveys a repository's issue and PR landscape from above — looking for hot spots, stale
pockets, and trends across the whole project rather than inspecting individual issues. Each
project's conventions (label taxonomy, staleness thresholds, milestone format, work-stream
ordering) are supplied declaratively through a per-repo manifest, so a single server serves any
repository without code changes.

The server reduces and computes; the caller renders. It fetches via the `gh` CLI, applies the
repository's manifest-declared conventions, and returns compact structured facts — leaving
narrative and presentation to the agent or tool driving it.

> Status: early development. Design and scope are still taking shape.

## License

[MIT](LICENSE)

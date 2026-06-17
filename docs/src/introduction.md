# Overstory

A generic, manifest-driven [MCP](https://modelcontextprotocol.io) server for GitHub project management.

Overstory surveys a repository's issue and PR landscape from above — hot spots, stale pockets, whole-project trends — and returns compact structured facts for the calling agent to render. It reduces and computes; the caller narrates and presents. A repository's conventions (label taxonomy, staleness thresholds, milestone format, work-stream ordering) are supplied declaratively through a per-repo manifest deep-merged over generic defaults, so a single server serves any repository without code changes.

This book is the user/integrator documentation: how to install the server, register it with an agent, author a manifest, and consume what the tools return. For the project's design rationale and contributor workflow, see `CLAUDE.md` and `CONTRIBUTING.md` in the repository root.

## Documentation Structure

- **Guide** — [Installation & Registration](./guide/installation.md): build and install the binary, the `gh`-authentication prerequisite, and how to register Overstory as an MCP server.
- **Reference**
  - [Manifests](./manifest.md) — how a repository's conventions are discovered, keyed, and deep-merged, plus the full block-by-block schema.
  - [Tools & Facts](./tools.md) — the two tools, their parameters, and the structured facts they return.

## Getting Started

Start with [Installation & Registration](./guide/installation.md) to get the server running, then author a manifest for the repositories you want to survey ([Manifests](./manifest.md)). The [Tools & Facts](./tools.md) reference is what a render-skill author consumes to turn the facts into a report.

# Overstory

A generic, manifest-driven GitHub project-management MCP server.

Overstory surveys a repository's issue and PR landscape from above — looking for hot spots, stale
pockets, and trends across the whole project rather than inspecting individual issues. Each
project's conventions (label taxonomy, staleness thresholds, milestone format, work-stream
ordering) are supplied declaratively through a per-repo manifest, so a single server serves any
repository without code changes.

The server reduces and computes; the caller renders. It fetches issues in-process from the
GitHub GraphQL API — authenticated with the operator's existing `gh` credentials — applies the
repository's manifest-declared conventions, and returns compact structured facts — leaving
narrative and presentation to the agent or tool driving it.

> Status: early development. Design and scope are still taking shape. The binary builds and serves over stdio, but exposes no tools yet — the issue-survey tools and manifest resolution land in upcoming changes.

## Development

Tool versions are managed by [mise](https://mise.jdx.dev/); tasks run through [just](https://github.com/casey/just):

```sh
mise install   # install pinned Go, golangci-lint, just, lefthook
just hooks     # install git hooks
just build     # build the binary to bin/
just test      # run tests
just lint      # run golangci-lint
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for scope, setup, and PR posture, and [CLAUDE.md](CLAUDE.md) for the design and key decisions.

## License

[MIT](LICENSE)

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

> Status: early development. Design and scope are still taking shape. The server speaks MCP over stdio and exposes seven tools — `backlog_review` (a grooming read of what in the backlog needs attention), `project_summary` (an orientation read of what's open and what to pick up), `milestone_tracks` (the within-milestone priority structure operators encode in a milestone's description), `authored_activity` (an attention read of what a given user authored and engaged with over a time window in one repo), `authored_activity_batch` (that attention read fanned out across a list of repos, one independent per-repo result each), `maintenance_activity` (a maintenance-attention read of the state mutations — relabeling, milestoning, closing/reopening, assigning, renaming — a given user paid to existing issues and PRs over a window in one repo), and `maintenance_activity_batch` (that maintenance read fanned out across a list of repos) — the first three resolve a single explicit `owner/repo` against that repo's manifest conventions, while the four `*_activity*` reads are author- and window-driven and read none.

## Usage

Install the binary and register it as an MCP server:

```sh
just install   # build and install to ~/.local/bin/overstory
```

Overstory authenticates to GitHub using your existing [`gh`](https://cli.github.com/) credentials (`gh auth token`) — there's no separate token to configure; just be logged in (`gh auth status`). The command an agent runs is the bare `overstory` binary (no arguments); conventions come from an operator-supplied per-repo manifest.

The full guide lives in the documentation book under [`docs/`](docs/):

- [Installation & Registration](docs/src/guide/installation.md) — building, the `gh` prerequisite, and the MCP server-config snippet.
- [Manifests](docs/src/manifest.md) — discovery, keying, deep-merge, and the block-by-block schema.
- [Tools & Facts](docs/src/tools.md) — the tools' parameters and the structured facts they return.

Build the book locally with `just docs-build` (or `just docs-serve` for live preview).

## Development

Tool versions are managed by [mise](https://mise.jdx.dev/); tasks run through [just](https://github.com/casey/just):

```sh
mise install   # install pinned Go, golangci-lint, just, lefthook, mdbook, mdbook-linkcheck2
just hooks     # install git hooks
just build     # build the binary to bin/
just test      # run tests
just lint      # run golangci-lint
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for scope, setup, and PR posture, and [CLAUDE.md](CLAUDE.md) for the design and key decisions.

## License

[MIT](LICENSE)

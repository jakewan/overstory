# Overstory — Agent Guide

This file orients an AI agent (or a new contributor) working in this repository. It is self-contained: everything needed to work here is described below or in the linked in-repo files.

## What Overstory is

A generic, manifest-driven [MCP](https://modelcontextprotocol.io) server for GitHub project management. It surveys a repository's issue and PR landscape from above — hot spots, stale pockets, whole-project trends — and returns compact structured facts for the calling agent to render.

The design splits two jobs that want different owners:

- **Mechanism** (this server's job): fetch a repository's issues and PRs from the GitHub GraphQL API (using the operator's `gh` credentials), apply that repository's conventions, reduce a large raw landscape to compact structured facts. Deterministic.
- **Judgment and presentation** (the caller's job): decide how to narrate and present the facts. The server renders nothing of its own.

Conventions (label taxonomy, staleness thresholds, milestone format, work-stream ordering) are supplied **declaratively** through a per-repo manifest, deep-merged over generic defaults — so a single server serves any repository without code changes. Repo targeting is explicit (`owner/repo`); there is no ambient default repository.

See `README.md` for the full design.

## Status and layout

The server exposes seven tools. The first three resolve a single explicit `owner/repo` against that repo's manifest conventions. `backlog_review` is the *grooming* read — what in the backlog needs maintenance attention — returning staleness, deferred, area-balance, quality, overlap, cross-reference, dependency, trajectory, and critical-path/gate blocks (the dependency block classifies open issues by their native blocked-by/blocking edges, convention-free; the critical-path/gate block appears when the repo's manifest declares a critical path). `project_summary` is the *orientation* read — given what's open now, what to pick up — returning milestone-progress, area-inventory, hygiene, open-PR, recommendation-input, dependency-classification, and critical-path/gate blocks (the dependency-classification block is the graph-level ready/blocked/gate split — the raw per-issue edges live in the recommendation-input block; the critical-path/gate block appears when the repo's manifest declares a critical path). `milestone_tracks` is the within-milestone *priority-structure* read — the ordered tracks (and their member issues) operators encode in a milestone's description — parsed declaratively per the repo's marker conventions. `authored_activity` is the *attention* read — given an explicit `owner/repo`, an author login, and a time window, the six decomposed counts (commits, issues/PRs opened, reviews submitted, PRs/issues engaged) of what that user authored and engaged with; it is author- and window-driven and reads no manifest conventions, and is the per-repo measure primitive a cross-project attention audit needs. `authored_activity_batch` is that primitive fanned out: given a *list* of `owner/repo`, it fetches each concurrently and returns one per-repo entry (the same counts, or an independent per-repo unavailability marker), so a cross-project audit measures many repos in one call rather than looping — one repo's failure degrades only its own entry, and an unresolvable author (repo-independent) is one whole-batch error. `maintenance_activity` is the *maintenance-attention* read — given an explicit `owner/repo`, an author login, and a window, the state mutations (relabeling, milestoning/demilestoning, deferral-labeling, closing/reopening, assigning, renaming) that author paid to existing issues and PRs, grouped per touched item most-recently-touched first — the grooming attention the authored counts structurally miss; also author- and window-driven and manifest-blind, and the first REST-sourced fetch (the events stream has no GraphQL equivalent), so the actor is matched by login string (an unknown login yields zero items, not an error) and the budget is the REST core pool, distinct from the authored reads' GraphQL points. `maintenance_activity_batch` is that read fanned out across a *list* of `owner/repo`, one independent per-repo entry each (the grouped items or a per-repo unavailability marker), with no whole-batch author error since the actor is a stream filter, not a resolved identity. The mirrored Claude/Cursor render skills arrive in their own changes.

```
cmd/overstory/        # binary entry point (constructs the MCP server, speaks stdio)
internal/server/      # MCP server construction, the tool contract, and the tools
internal/manifest/    # per-repo convention resolution (deep-merged over generic defaults)
internal/github/      # in-process GitHub GraphQL data layer (issues, milestones, PRs)
internal/reduce/      # reduction primitives shared by backlog and summary (label matcher, day math)
internal/backlog/     # the grooming reduction (pure functions, structured facts)
internal/summary/     # the orientation reduction (pure functions, structured facts)
internal/criticalpath/# the critical-path / gate reduction shared by both tools (pure functions, structured facts)
internal/dependency/  # the native-dependency reduction shared by both tools — ready/blocked/gate classification over blocked-by/blocking edges (pure functions, structured facts)
internal/authored/    # the authored-activity reduction (pure functions, structured facts)
internal/maintenance/ # the maintenance-activity reduction — actor state-mutations over the REST issue-events stream (pure functions, structured facts)
```

Further reductions and the packages they need arrive in their own changes — do not create packages speculatively; add them when a change needs them.

## Build, test, lint

Tool versions are managed by [mise](https://mise.jdx.dev/) (`mise.toml`, with checksums in the committed `mise.lock` — regenerate it with `mise lock` whenever a pin changes); tasks run through [just](https://github.com/casey/just) (`justfile`). One-time setup:

```sh
mise install     # install pinned Go, golangci-lint, just, lefthook, mdbook (+ linkcheck2 on Linux)
just hooks       # install git hooks (lefthook)
```

Everyday commands:

```sh
just build       # build the binary to bin/
just test        # go test ./...
just lint        # golangci-lint run ./...
just fmt         # gofmt -w .
just tidy        # go mod tidy
just tidy-check  # fail if go.mod/go.sum are not tidy (CI runs the same check)
just verify      # go mod verify
just vuln        # govulncheck over dependencies and the standard library
just toolchain-outdated  # report mise-managed tools with newer versions
just install     # build and install to ~/.local/bin
just docs-build  # build the documentation book to docs/book/
just docs-serve  # serve the documentation book locally with live reload
```

User/integrator documentation lives in the `docs/` mdbook (`docs/src/`); the generated `docs/book/` is gitignored. See `CONTRIBUTING.md` for the docs maintenance contract.

Formatting is enforced by golangci-lint's configured formatters (`gofmt`, `goimports`) — there is no separate format-check step. The `lefthook` hooks run formatting on commit and lint/test on push.

## Development approach

This project uses [BDD][bdd]-style/outside-in [TDD][tdd] for non-trivial code: write a failing behavior test from the caller's perspective first, let it drive the API, then implement the minimum to pass and refactor under the test's safety net. Tests use the standard `testing` package (no external frameworks), favor table-driven cases, exercise tool behavior through an in-memory MCP client/server session, and isolate filesystem state with `t.TempDir()`. Skip the ceremony for trivial work (typos, single-line fixes, documentation, these instruction files).

Go authoring conventions are in `.claude/rules/go-practices.md` (loaded when editing Go).

## Key design decisions

- **Single binary, daemonless.** It serves a session over stdio and exits. No background process, no network service.
- **MCP over stdio is JSON-RPC.** stdout carries the protocol and nothing else — send diagnostics to stderr (`log`), never to stdout. Exiting on stdin EOF is normal shutdown.
- **The server reduces; the caller renders.** Tools return compact structured facts, not prose or pre-rendered markdown. Presentation and narrative judgment live in the calling agent. This boundary is load-bearing — it is what lets one server serve many callers (Claude, Cursor) and many rendering styles.
- **Conventions are declarative, not hardcoded.** A repository's label taxonomy, thresholds, and milestone format come from a per-repo manifest deep-merged over generic defaults — never from Go constants. This is what makes the server generic across repositories.
- **Manifests are operator-supplied, not in the target repo.** A repo's conventions are discovered from the operator's own config — every `*.yml`/`*.yaml` in `$XDG_CONFIG_HOME/overstory/manifests.d/`, or a colon-separated `OVERSTORY_MANIFESTS` file list — keyed by `owner/repo` and deep-merged over generic defaults. This lets the server survey *arbitrary* repos (including ones the operator doesn't control) without those repos adopting anything. The public/private split is deliberate and is a *metadata*-leak concern, not secrets — so the answer is **layering, not encryption**: commit a public manifest, keep private/work-org entries in a gitignored `*.private.yml` or a file outside any repo (named only via `OVERSTORY_MANIFESTS`), so private repo names never enter public config. This layering composes the *file set* — public repos' entries in the committed file, private repos' entries in the gitignored one — with each repo's entry living **whole in exactly one file**. Splitting a single repo's entry across files is not supported: a key that appears in more than one discovered file (or twice, case-insensitively, within one) is a hard configuration error, so a convention can never be silently dropped by file ordering.
- **GitHub data is fetched in-process from the GraphQL API**, with credentials sourced from the operator's `gh` CLI (`gh auth token`) — so the server inherits existing `gh` authentication (no separate token to configure) without a subprocess per fetch. `gh` is shelled out to only for that credential bootstrap.

## Conventions in this repo

- `.claude/rules/go-practices.md` — Go authoring conventions (path-conditioned to Go files).
- `.claude/rules/pr-conventions.md` — PR descriptions, commit format, changelog policy, branch freshness, fix-vs-defer.
- `.claude/rules/pr-waste-patterns.md` — what counts as reviewer-distracting waste in a diff.
- `.claude/rules/no-personal-details.md` — keep personal/identifying details out of this public repo.
- `.claude/rules/markdown-practices.md` — authoring conventions for the docs book (path-conditioned to `docs/src/`).
- `.claude/rules/toolchain-ci-parity.md` — keeping the pinned local toolchain and CI in lockstep.
- `.claude/rules/design-fork-adjudication.md` — how value-laden design forks are settled here.
- `CONTRIBUTING.md` — contributor setup, scope, and PR posture.
- `SECURITY.md` — reporting channel, plus the credential, supply-chain, and data-handling claims. It asserts how the server treats the `gh`-sourced token, so a change to `internal/github/token.go` or the request path should be checked against it; it also asserts what CI scans, pins, and verifies, so a change to the workflows, `mise.lock`, or the Dependabot config should be checked against it too.
- `CODE_OF_CONDUCT.md` — Contributor Covenant 2.1, with conduct reports routed the same way security reports are.
- `.github/copilot-instructions.md` — review guidance for GitHub Copilot.

[bdd]: https://en.wikipedia.org/wiki/Behavior-driven_development
[tdd]: https://en.wikipedia.org/wiki/Test-driven_development

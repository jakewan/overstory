# Contributing to Overstory

## Issues

This project uses **problem-framed issues**. The issue template asks you to describe:

- **The problem** you're experiencing
- **Current behavior** — what happens today
- **Desired behavior** — what you'd expect instead
- **Why it matters** — the impact on your workflow

Focus on describing the problem clearly. Solution ideas are welcome as supplementary context, but the issue should stand on the strength of the problem description alone.

## Scope

Overstory is a single-purpose MCP server: it surveys a GitHub repository's issue and PR landscape and returns compact structured facts for the caller to render, applying each repository's conventions from a declarative manifest. It reduces and computes; it renders nothing of its own, and it hardcodes no repository's conventions.

Contributions should stay within this focused scope. If you're unsure whether something fits, open an issue describing the problem first.

## Development

### Setup

Tool versions are managed by [mise](https://mise.jdx.dev/). After cloning:

```bash
mise install        # Install Go, golangci-lint, just, lefthook, mdbook, mdbook-linkcheck2
just hooks          # Install git hooks (lefthook)
```

Tool versions come from `mise.toml`, and their checksums from the committed `mise.lock` — `mise install` verifies downloads against it. If you change a version pin, run `mise lock` and commit the updated lockfile in the same change.

One platform caveat: the documentation toolchain is Linux-only today. `mdbook-linkcheck2` publishes an `x86_64-unknown-linux-gnu` binary and no others, so a bare `mise install` cannot provision it on macOS or arm64. Name the tools you need instead — `mise install go just golangci-lint lefthook` covers everything except `just docs-build`, which needs a Linux x86-64 machine or a container.

### Build, Test, Lint

All commands go through [just](https://github.com/casey/just):

```bash
just build              # Build binary to bin/
just test               # Run all tests
just lint               # Run golangci-lint
just vuln               # Scan dependencies and stdlib for known vulnerabilities
just tidy-check         # Fail if go.mod/go.sum are not tidy
just toolchain-outdated # Report mise-managed tools with newer versions
just install            # Install the binary to ~/.local/bin
```

### Dependencies and the toolchain

Go modules are watched by Dependabot and scanned by `govulncheck` in CI (on every change, and weekly). The mise-managed toolchain has no update bot — no ecosystem covers `mise.toml` — so it is reviewed by hand, prompted by the weekly scan's outdated-tool report, by a CI toolchain failure, or by release preparation. `just toolchain-outdated` runs that check locally.

Several pins move in pairs; `.claude/rules/toolchain-ci-parity.md` records which and why. `SECURITY.md` describes the full supply-chain posture, including what the scanning does and does not guarantee.

### Testing Approach

The project uses BDD-style/outside-in TDD:

- Write failing tests before production code.
- Drive the MCP tool surface from acceptance tests that exercise the server over an in-memory client/server session, then build inward.
- Tests use the standard `testing` package — no external test frameworks.
- Use table-driven tests for multiple scenarios; isolate filesystem state with `t.TempDir()`.

### Documentation

User/integrator documentation lives in the [`docs/`](docs/) book (mdbook). Build it with `just docs-build` (or `just docs-serve` for live preview) before pushing documentation changes. `docs-build` also runs the linkcheck backend, so a broken `SUMMARY.md` entry, cross-page link, or anchor fails the build. CI enforces the same build on any change under `docs/`, and a pre-push hook runs it locally when a push includes docs changes.

Keep the docs pinned to the code. Overstory's observable output is taught across the whole book, so a change to a tool's output — a block added, removed, or changed in meaning; a documented field's meaning; a truncation contract; the shape of what a tool returns — triggers a review of every doc surface that teaches it, not only the specific files below. Treat the list as signposts, not an exhaustive checklist:

- When a tool is added, renamed, or changes its parameters, update its mention in `README.md` and `docs/src/tools.md`.
- When the install or registration path changes, update `docs/src/guide/installation.md` — its single home; the README only links to it.
- The tool/fact reference documents the stable shape and points at the Go source (`internal/backlog/`, `internal/summary/`) for field-by-field detail, so adding a field to a `Facts` struct doesn't require a doc edit. Update the reference only when a block is added, removed, or changes meaning.
- The reference render skills under `docs/src/guide/render-skills/` reproduce a skill's body verbatim, so — unlike the reference above — they name individual fields (`sizeBound`, `blockedByTruncated`, and the like). They are the one exception to the field-doc-freedom rule: when a block or a named field's meaning changes, revisit the guides too, not just the reference. Overstory owns this render-skill content canonically — a maintainer's own working-copy skills track it, so corrections start here and flow outward, never the reverse.

### Project name

Write **Overstory** (capitalized) when naming the project in prose — titles, headings, and references, at a sentence start or mid-sentence. Use lowercase **overstory** only for the literal identifier token: the binary (`cmd/overstory`), Go package and import paths, the `mcpServers` config key, the registry name, the `OVERSTORY_MANIFESTS` environment variable and `…/overstory/…` config paths, and runtime strings (the `log` prefix, the GitHub user-agent). This mirrors the MCP convention of a lowercase machine `name` paired with a Title-Case display `title`: the server registers `name: overstory`, `title: Overstory`.

## Pull Requests

- Keep PRs small and focused — each PR should serve a single purpose.
- PRs are squash-merged, so commit history within a branch doesn't need to be pristine.
- This project merges, never rebases.

# Contributing to overstory

## Issues

This project uses **problem-framed issues**. The issue template asks you to describe:

- **The problem** you're experiencing
- **Current behavior** — what happens today
- **Desired behavior** — what you'd expect instead
- **Why it matters** — the impact on your workflow

Focus on describing the problem clearly. Solution ideas are welcome as supplementary context, but the issue should stand on the strength of the problem description alone.

## Scope

overstory is a single-purpose MCP server: it surveys a GitHub repository's issue and PR landscape and returns compact structured facts for the caller to render, applying each repository's conventions from a declarative manifest. It reduces and computes; it renders nothing of its own, and it hardcodes no repository's conventions.

Contributions should stay within this focused scope. If you're unsure whether something fits, open an issue describing the problem first.

## Development

### Setup

Tool versions are managed by [mise](https://mise.jdx.dev/). After cloning:

```bash
mise install        # Install Go, golangci-lint, just, lefthook
just hooks          # Install git hooks (lefthook)
```

### Build, Test, Lint

All commands go through [just](https://github.com/casey/just):

```bash
just build    # Build binary to bin/
just test     # Run all tests
just lint     # Run golangci-lint
just install  # Install the binary to ~/.local/bin
```

### Testing Approach

The project uses BDD-style/outside-in TDD:

- Write failing tests before production code.
- Drive the MCP tool surface from acceptance tests that exercise the server over an in-memory client/server session, then build inward.
- Tests use the standard `testing` package — no external test frameworks.
- Use table-driven tests for multiple scenarios; isolate filesystem state with `t.TempDir()`.

## Pull Requests

- Keep PRs small and focused — each PR should serve a single purpose.
- PRs are squash-merged, so commit history within a branch doesn't need to be pristine.
- This project merges, never rebases.

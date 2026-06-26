# PR and Commit Conventions

How pull requests, commits, and the changelog work in this project.

## PR Descriptions

(extension point: `pr-description-format`)

Structure a PR body as:

- **Overview** — the purpose: what the change accomplishes and why. Lead with this.
- **How it works** (optional) — only for non-obvious mechanics a reviewer cannot infer from the diff.
- **Issue references** — closing keywords (`Closes #N`); repeat the keyword for each issue (`Closes #1, closes #2`).

Avoid:

- Enumerating the diff file-by-file — the diff already shows what changed.
- Narrating the drafting journey ("earlier this did X, then I changed it").
- Scaffolding headers with no content under them.
- Hard-wrapping prose at a fixed column. Write one long line per paragraph and let the renderer wrap.

## Commit Messages

(extension point: `squash-commit-format`)

Conventional Commits: `type(scope): subject`.

- **Types**: `feat`, `fix`, `refactor`, `docs`, `build`, `ci`, `test`, `chore`.
- **Scopes** (this project's areas): `mcp`, `manifest`, `backlog`, `gh`, `ci`, `build`, `docs`, `rules`, `dx`, `deps`. `mcp` covers the server and tool contract; `manifest` the declarative convention resolution; `backlog` the issue-reduction logic; `gh` the GitHub-CLI fetch layer; `dx` developer-experience work (tooling, justfile, hooks); `deps` dependency updates (e.g., Dependabot bumps). A changelog-only commit — notably the post-PR-creation commit that anchors an entry to its PR number — belongs to no code area; scope it `docs` with no scope (`docs: ...`).
- **Body**: short prose stating *why* — the motivation, constraint, or problem solved — sized to the change, not its diff. A small change may need a one-line body or none. Don't restate the diff, don't narrate the journey ("the review surfaced...", "earlier this did X"), and don't re-derive rationale a durable doc (a design decision in `CLAUDE.md`, a doc comment) already records — point to it or omit it. State the durable *why* once, concisely.
- **Issue references**: `Closes #N` (or `Related to #N`); repeat the keyword per issue.

## Changelog

(extension point: `changelog-convention`)

This project keeps a changelog in `CHANGELOG.md` following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

A PR requires a changelog entry when it makes a **user-facing change**:

- New, changed, or removed MCP tools.
- Observable behavior changes (reduction results, validation, output shape).
- Bug fixes that affect user-visible results.
- Manifest-format or output-shape changes that affect configuration or returned data.

No entry is needed for: internal refactors with no observable effect, test-only changes, CI/build/tooling, documentation, or agent rules and skills.

Add the entry under `## [Unreleased]` in the matching category — `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, or `Security`. Keep it concise, user-facing, and present-tense.

Anchor each entry to its introducing PR as a trailing `(#N)` — the PR number, not the issue (the related issue is linked from the PR body instead). Because the number isn't known until the PR exists, the entry can be authored with the tracking-issue number and corrected to the PR number once it's created.

When a PR corrects or refines the behavior of a feature still under `[Unreleased]`, amend that feature's existing entry in place rather than adding a separate `Fixed`/`Changed` line — you don't log a fix for behavior that never shipped, and `[Unreleased]` should describe what will actually ship. The PR's changelog footprint is the in-place refinement; it carries no standalone `(#N)` anchor of its own.

## Branch Freshness

(extension point: `freshness-response-policy`)

When assessing how far a branch trails its base:

- **Up to date** — proceed.
- **Modestly behind** — note it; refreshing is optional.
- **Significantly behind, or conflicts are likely** — merge the base branch in first (this project merges, never rebases) before merging the PR.

## Code-Audit Fix vs. Defer

(extension point: `code-audit-practice`)

When acting on review findings:

- **Fix in place** when the finding is small, localized, and within the PR's stated purpose.
- **Defer to a GitHub issue** when addressing it would expand the PR's scope or is tangential to its purpose.

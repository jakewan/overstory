---
paths:
  - "docs/src/*.md"
  - "docs/src/**/*.md"
---

# Markdown Practices

Conventions for the user/integrator documentation book (`docs/src/`). These keep the book buildable and consistent at authoring time, so the `mdbook build` + linkcheck guard (local pre-push and CI) rarely has to reject a change. The guard is the backstop; this rule is the first line.

## Structure

- Every page must be reachable from `SUMMARY.md`. When adding a page, add its entry; mdbook fails the build on a `SUMMARY.md` entry pointing at a missing file, and a page absent from `SUMMARY.md` is silently unpublished.
- One `#` (H1) per page — the page title. Nest with `##` and below; don't skip levels.

## Links

- Link between pages by their source `.md` path, relative to the current file (e.g. `[Installation](./guide/installation.md)`). mdbook rewrites `.md` to `.html` at build; the linkcheck2 backend validates that the target file — and any `#anchor` — resolves, so a broken cross-reference fails the build.
- Anchors are mdbook's slugified headings (lowercase, spaces to hyphens). Link to a heading with `#the-heading-text`; if you rename a heading, fix the links that target it.

## Prose

- Write each paragraph as one long line; let the renderer wrap. Don't hard-wrap prose at a fixed column — this matches the PR/commit convention in `pr-conventions.md`.

## Reproducing verbatim content

- When a page reproduces content that itself contains triple-backtick code fences — the reference render skills under `docs/src/guide/render-skills/` reproduce a skill's `SKILL.md` body verbatim — wrap the reproduction in a **four-backtick** outer fence instead of the usual three. A three-backtick outer fence closes at the first inner triple-backtick and the rest of the body mis-renders as page structure. linkcheck2 validates links and anchors, not fence nesting, so the build will not reject this — the page just renders wrong, which makes the convention recall-dependent rather than build-enforced.

## Tool and fact reference

- The tool/fact reference documents the stable *shape* and points at the Go source (`internal/backlog/`, `internal/summary/`) for field-by-field detail. Don't duplicate struct fields into prose — adding a field to a `Facts` struct shouldn't require a doc edit. Update the reference only when a block is added, removed, or changes meaning (see `CONTRIBUTING.md`).
- The reference render skills under `docs/src/guide/render-skills/` are the exception: because they reproduce a skill's body verbatim, they *do* name individual fields, so a block or named-field change requires revisiting them too. Overstory maintains this render-skill content canonically — see `CONTRIBUTING.md` for the full docs-pinning contract.

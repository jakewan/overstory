---
paths: docs/src/**/*.md
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

## Tool and fact reference

- The tool/fact reference documents the stable *shape* and points at the Go source (`internal/backlog/`, `internal/summary/`) for field-by-field detail. Don't duplicate struct fields into prose — adding a field to a `Facts` struct shouldn't require a doc edit. Update the reference only when a block is added, removed, or changes meaning (see `CONTRIBUTING.md`).

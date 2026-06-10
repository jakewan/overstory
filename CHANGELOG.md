# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `backlog_review` now also returns an area-balance reduction: the distribution of open issues across a repository's functional areas, with first-class counts of unclassified (un-area-labeled) and multi-area issues, ordered by count. Areas are identified by manifest conventions under `areaBalance` — an explicit `labels` list and/or `prefixes` rules (each a `prefix` plus a configurable `delimiter`, e.g. `area` + `/` for `area/networking`), matched case-insensitively and unioned, with casing/source variants of one area collapsed into a single bucket. Unlike deferred review, area balance ships generic default prefixes (`area/`, `area:`, `area-`), so a repository with no `areaBalance` block still classifies common `area/*`-style labels out of the box; an explicit `prefixes: []` disables them. Per-area counts may overlap, since a multi-area issue counts in each area.
- `backlog_review` now also returns a deferred-issue review: open issues carrying a maintainer-declared "deferred" label (e.g. `deferred`, `blocked`, `icebox`) reduced to compact facts — a count, the parked issues (number, title, URL, the labels matched, days since last human activity, age), the configured labels echoed back, and explicit list/fetch truncation seams — ordered most-inactive first. Deferred labels are repo-specific and declared per repository in the manifest under a `deferred.labels` list; there is no generic default, so a repo that declares none reports the block as not-configured rather than guessing. Labels match case-insensitively. (#8)
- `backlog_review` MCP tool: given an explicit `owner`/`repo`, returns compact structured staleness facts — an exact open-issue count, counts by inactivity band, the stalest issues (number, title, URL, days since last human activity), and the threshold applied with its source (a repository's manifest entry deep-merged over generic defaults, or the defaults). Inactivity is measured from the last non-bot comment — ignoring label, assignment, and bot noise — falling back to creation. List and fetch limits are surfaced explicitly and never silently truncated. (#4)
- GitHub access: issues are fetched in-process via the GitHub GraphQL API, authenticated with the operator's existing `gh` credentials (`gh auth token`) — no separate token configuration. GitHub.com only for now. (#4)
- Manifest resolution: per-repo manifests are discovered from `$XDG_CONFIG_HOME/overstory/manifests.d/*.yml` (and `*.yaml`), or from a colon-separated file list in `OVERSTORY_MANIFESTS` when set; entries are keyed by `owner/repo` and deep-merged over generic defaults. A repo's `owner/repo` key must be well-formed and defined exactly once across the discovered files — a malformed key (e.g. containing whitespace) or a duplicate (across files or case-insensitively within one file) is a hard configuration error rather than a silent fallback or drop. (#4, #6)

### Changed

- `backlog_review` output is now a composite of named reduction blocks. Review-level identity (`repo`, `generatedAt`) stays at the top level; the staleness facts move under a `staleness` block, and the new deferred-issue facts arrive under a `deferred` block. Callers that read staleness fields at the top level must now read them under `staleness`. (#8)

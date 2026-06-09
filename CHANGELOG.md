# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `backlog_review` MCP tool: given an explicit `owner`/`repo`, returns compact structured staleness facts — an exact open-issue count, counts by inactivity band, the stalest issues (number, title, URL, days since last human activity), and the threshold applied with its source (a repository's manifest entry deep-merged over generic defaults, or the defaults). Inactivity is measured from the last non-bot comment — ignoring label, assignment, and bot noise — falling back to creation. List and fetch limits are surfaced explicitly and never silently truncated. (#4)
- GitHub access: issues are fetched in-process via the GitHub GraphQL API, authenticated with the operator's existing `gh` credentials (`gh auth token`) — no separate token configuration. GitHub.com only for now. (#4)
- Manifest resolution: per-repo manifests are discovered from `$XDG_CONFIG_HOME/overstory/manifests.d/*.yml` (and `*.yaml`), or from a colon-separated file list in `OVERSTORY_MANIFESTS` when set; entries are keyed by `owner/repo` and deep-merged over generic defaults. (#4)

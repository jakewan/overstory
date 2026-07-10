# Reference Skill: Backlog Review

A worked render skill for the `backlog_review` tool — the grooming read ("what in the backlog needs maintenance attention?"). It renders the staleness, deferred, area-balance, quality, overlap, cross-reference, dependency-structure, trajectory, and critical-path blocks into a report for a dedicated grooming session, then synthesizes prioritized findings. See [Rendering the Facts](../rendering.md) for the concept this skill makes concrete, and [Project Summary](./project-summary.md) for its lighter-weight orientation counterpart.

> **Reference skill.** This is the reference render skill for `backlog_review`, maintained in this repository. It is an illustrative example, not a fixed interface: the conventions evolve with the server's reductions, so adapt it to your repository. To use it, copy the body below into `~/.claude/skills/pm-backlog-review/SKILL.md` (or `~/.cursor/skills/pm-backlog-review/SKILL.md` for Cursor) and adjust as needed. Last revised 2026-07.

````markdown
---
name: pm-backlog-review
description: >-
  Activate when the user wants a deep backlog health review of a GitHub
  repository — trigger phrases include "backlog review", "groom the backlog",
  "review backlog health", "what issues are stale", "review deferred issues",
  "check for duplicate issues", or "backlog analysis". Renders the overstory MCP
  server's structured backlog facts — staleness, deferred issues, area balance,
  issue quality, title overlaps, cross-reference clusters, and backlog trajectory
  — into a report for discussion. For dedicated grooming, not a quick status
  check.
---

# Backlog Review

Comprehensive backlog health analysis for dedicated grooming sessions. The overstory MCP server does the fetching and reduction; this skill renders its structured facts into a report, then transitions to collaborative decision-making.

The server returns **pure structured facts** — no prose, no markdown. Every section below is rendered client-side from those facts. The skill is generic: it carries no repo's conventions. A repo's specifics (which labels are areas, which mean deferred, its staleness threshold) come from the server's manifest, surfaced in the facts; this skill never hardcodes them.

## Step 1: Call the tool

Call the overstory MCP tool with an explicit high limit:

```text
mcp__overstory__backlog_review(owner=<owner>, repo=<repo>, limit=100)
```

Resolve `owner` and `repo`: if the user named a repository, use that `owner/repo` directly. Otherwise resolve from the current directory (requires `gh`):

```bash
gh repo view --json owner,name --jq '"owner=\(.owner.login) repo=\(.name)"'
```

Pass the two resolved values to the tool's `owner` and `repo` parameters.

`limit` caps how many items each block *lists* (stale issues, deferred issues, quality-flagged issues, overlap groups, cross-reference groups) — 100 is the maximum and the right value for deep grooming; the default of 20 would render-truncate a real backlog. Coverage of how many issues are *examined* is governed by the manifest's fetch limits, independent of `limit`, so a high `limit` does not change what the server scanned — only how much it shows.

### If the tool is unavailable

If `mcp__overstory__backlog_review` is not callable, stop and emit a short precondition guard rather than improvising an analysis. Check, in order:

- Is the `overstory` binary on `PATH`? (`command -v overstory`)
- Is the overstory server registered in the active MCP config for this tool?
- Has the session been restarted since the server was installed or registered? The stdio server spawns at session start — a mid-session install is not picked up until restart.

Report which check failed and stop. This skill has **no `gh` fallback** by design: overstory is the single source of the reductions, and a hand-rolled `gh` analysis would silently diverge from what the server computes.

## Step 2: Render the report

The tool returns a composite object: `repo`, `generatedAt`, one block per section — `deferred`, `quality`, `staleness`, `areaBalance`, `overlap`, `crossRef`, `dependencies`, `trajectory`, `criticalPath` — a top-level `openIssueSet` (used to resolve deferred `bodyRefs`, not rendered as its own section), and an optional `rateLimit`. Render each section below from its block, in order, then synthesize Key Findings last.

**Truncation is load-bearing.** Several blocks carry `fetchTruncated` (the scan window didn't cover every open issue), `listTruncated` (more matches exist than were listed), and — on `crossRef` — `refsTruncated` (some issues' reference lists were capped). The open-issue fetch paginates the *full* open set, so `fetchTruncated` normally fires only when a repository exceeds the `staleness.fetchLimit` safety backstop — uncommon on most backlogs. When any flag is true, render an explicit "lower bound" note for that section: the result is a floor, not a complete picture. Never present a truncated run as exhaustive. For overlap and cross-reference specifically, `fetchTruncated` means duplicates or links *outside* the fetch window are undetectable — say so. Deferred issues additionally carry per-edge flags (`blockedByTruncated`, `blockingTruncated`, `subIssuesTruncated`) and `openIssueSet` carries `fetchTruncated` — an empty edge list under a true flag is a floor, not a confirmed "none." Additionally, the response as a whole carries a top-level `sizeBound` marker **only when** it was trimmed to a serialization byte budget — `finalBytes`/`maxBytes` plus a `trimmedBlocks[]` of `{block, dropped, remaining}` (where `block` may be a nested path, e.g. `hygiene.stale`). Each trimmed block also sets its own `listTruncated`, so the per-block lower-bound note still fires; `sizeBound` adds the *cause* (a size trim, not a `limit` cap) and the count dropped. When present, flag the response as size-bounded and note the remedy is a narrower request (fewer `blocks`) or a higher per-repo `maxBytes`, not a higher `limit`.

### Deferred Issue Review

Header: `## Deferred Issue Review`

From the `deferred` block. If `configured` is false, the repo declares no deferred labels — render "No deferred-label convention configured for this repo" and move on. Otherwise list the `deferredIssues`, each with its `number`, `title`, `matchedLabels`, `inactiveDays`, `ageDays`, and its dependency signals when present — native `blockedBy` (open blockers gating this issue), `blocking` (open issues it gates), the open sub-issue gap (`subIssuesTotal − subIssuesCompleted`, which gates even when `subIssues[]` is empty), and `bodyRefs` (stated `#N` deps, resolved against `openIssueSet.numbers`):

```markdown
- #42 - Title — labels: `deferred`; inactive 71d, age 120d; blocked by open #30; stated deps: #31 (open), #33 (unresolved)
```

Sort longest-inactive first. Note `configuredLabels` so the reader knows what was matched. If `deferredCount` is 0: "No deferred issues — N open issues carry none of the configured deferred labels." If `listTruncated`, note that more deferred issues exist than listed.

Overstory reduces deferred issues to their parked-state facts (which labels, how long inactive, how old) plus dependency signals. It does **not** assess premise validity or missing rationale — those are judgment reads the old per-repo skills did by hand. `blockedBy`/`blocking`/`subIssues` are GitHub's **authoritative native edges** (open-filtered); a non-empty open `blockedBy` or a positive sub-issue gap means the deferred issue is still blocked. `bodyRefs` surfaces an issue's stated `#N` dependencies when present — a heuristic floor (parsed from rendered plaintext, so a `#N` in a code fence won't appear), not native edges. Resolve each `bodyRef` against this call's `openIssueSet.numbers`: ∈ numbers confirms a still-open dependency; ∉ numbers is **unresolved/indeterminate** (a closed issue, an open PR, cross-repo, or — under `openIssueSet.fetchTruncated` — beyond the window), **never** "resolved." This section is a pure render — it surfaces gate facts and issues no ready/not-ready verdict (that ranking is `pm-project-summary`'s and `pm-milestone-lifecycle`'s). Don't fabricate the rest; if the user wants that depth, offer to read specific issue bodies during discussion.

### Issue Quality Audit

Header: `## Issue Quality Audit`

From the `quality` block. Render the headline counts (`missingBodyCount`, `noLabelsCount`, `flaggedCount` of `openIssueCount`), then list `flaggedIssues`. Each issue carries `bodyLength`, `missingBody`, `labelCount`, `noLabels`, and `missingCategories`:

```markdown
- #15 - Title — body 12 chars (below minBodyLength N); no labels
```

`minBodyLength` is the threshold the body check used. The missing-required-category check runs only when `categoriesConfigured` is true; when it's false (`configuredCategories` empty), that sub-check is inert — render only the thin-body and no-labels findings, and don't imply a category check ran. If `flaggedCount` is 0: "No quality issues flagged." If `listTruncated`, note more flagged issues exist than listed.

### Staleness Analysis

Header: `## Staleness Analysis`

From the `staleness` block. Lead with the threshold and its source: `thresholdDays` days, `thresholdSource` either `manifest` (a manifest entry matched this repo — note the value may still be overstory's default if that entry didn't override the threshold) or `default` (no entry matched; built-in threshold). Render the inactivity-band distribution from `buckets` (each has `minDays`, `maxDays`, `count`; a bucket with `maxDays` of 0 is the open-ended top band — render it as "`minDays`+ days"), then list `staleIssues` (each with `number`, `title`, `inactiveDays`, `ageDays`), longest-inactive first:

```markdown
**Threshold: 45 days (source: manifest)** — N stale of M open

| Inactive band | Count |
| ------------- | ----- |
| 45–90 days    | 4     |
| 90+ days      | 2     |

- #23 - Title — inactive 96d, age 210d
```

**Deferred issues are already excluded.** Overstory's staleness reduction now omits intentionally-parked issues server-side — `staleIssues` and `staleCount` count only neglected work, and `deferredExcludedCount` reports how many deferred issues were left out. Surface that count when non-zero ("N deferred issues excluded as parked"); no client-side suppression is needed. Overstory cannot partition by milestone or exclude epics from staleness (no such field on a stale issue), so — unlike the old per-repo skills — this report does not separate "stale with milestone" from "stale without," and does not exclude tracking issues. Flag that as a known gap if the backlog has epics. If `staleCount` is 0: "Backlog is fresh — no issues past the staleness threshold." Note `fetchTruncated` / `listTruncated` if set.

### Area Balance

Header: `## Area Balance`

From the `areaBalance` block. Render `areas` (each `area` + `count`), plus `unclassified` and `multiAreaCount`, against `openIssueCount`:

```markdown
| Area        | Open | Share |
| ----------- | ---- | ----- |
| ghostty     | 8    | 47%   |
| ai-skills   | 4    | 24%   |
| unclassified| 5    | 29%   |
```

Compute share as `count / openIssueCount`. **Per-area counts can overlap** — a multi-area issue counts in each area — so shares need not sum to 100%; note this when `multiAreaCount > 0`. The highest-count area is the focus area (a confirmation signal during active work, not "overloaded"). Two honest limits to state when relevant: overstory returns only areas with at least one open issue, so a declared-but-empty area is invisible here (the report cannot mark areas "starved"); and it does not split each area into active-vs-deferred, so the table is total open issues per area. If `areas` is empty, every open issue is unclassified — render that plainly (it may mean the repo has no area-label convention, which is fine). Note `fetchTruncated` if set.

### Possible Overlaps

Header: `## Possible Overlaps`

From the `overlap` block. These are conservative candidates for human judgment — never declare duplicates. List `groups`, each a set of `issues` (number/title) sharing `sharedTokens`:

```markdown
- #42 "Title A" ↔ #87 "Title B" — shared: deferred, label, harvest; verify scope boundaries
```

Lead with `overlappingCount` of `openIssueCount` and the `titleThreshold` used. Overlap is computed over the *fetched* window and over *open* issues only — it cannot surface "possibly already resolved" against closed issues (a signal the old skills approximated). If `groupCount` is 0: "No title overlaps detected at threshold N." If `fetchTruncated` or `listTruncated`, note overlaps outside the window are undetectable.

### Cross-Reference Clusters

Header: `## Cross-Reference Clusters`

From the `crossRef` block. List `groups` — clusters of open issues linked issue-to-issue. Each group has `issues` (number/title) and directed `references` (`from` → `to`):

```markdown
- Cluster: #23, #45, #67 — links: #23→#45, #45→#67; coordinated work or duplicated scope?
```

Lead with `linkedCount` of `openIssueCount` and `largestGroupSize`. Briefly read each cluster as coordinated work (fine) or possible duplicated scope (worth attention). If `groupCount` is 0: "No cross-reference clusters." Note `fetchTruncated` / `listTruncated` / `refsTruncated` if set — a capped reference list means some links were not seen.

`crossRef` links are issue-to-issue *mentions*, and citation direction routinely inverts dependency direction (a capstone cites the work it depends on, reading as though it gates them). For the **authoritative** blocked-by/blocking direction, see Dependency Structure below — read a cluster against it before treating a citation hub as a gate.

### Dependency Structure

Header: `## Dependency Structure`

From the `dependencies` block. These are GitHub's **authoritative native** blocked-by/blocking edges (plus sub-issue hierarchy) across *all* open issues, convention-free — so they surface even on a repo with no deferred convention, correcting the mention-direction inversion `crossRef` can present. The "gates" here are **native-edge do-first roots** (a ready issue that blocks open downstream work) — a different notion from the manifest critical-path *stream* gate in the Critical Path / Gate Status section below; don't conflate the two.

Lead with the ready/blocked/provisional split over `fetchedCount` (the classification partitions the *fetched* window), carrying `openIssueCount` as the repo-wide open total:

```markdown
**Dependencies: 5 ready / 3 blocked / 1 provisional** — 9 of 40 open classified
```

When `fetchTruncated`, the split covers only the fetched window — the remaining `openIssueCount − fetchedCount` open issues are **unclassified** (a coverage floor); say so, and never present the split as covering `openIssueCount` (the three counts sum to `fetchedCount`, not the repo total). Unlike `criticalPath`, each *fetched* issue's classification is authoritative — `fetchTruncated` bounds coverage, it does not degrade the fetched verdicts.

**Gates** — the do-first roots, most-downstream-first (`gates`, each `number`, `title`, `blocking`):

```markdown
- #42 - Title — unblocks 4 open (blocking #51, #52, #58, #61)
```

If `gatesTruncated`, render `gateCount` as the authoritative total ("12 gates, showing 10") — not a bare "more exist." Honor per-issue `blockingTruncated` (a gate's `blocking` list, hence its "unblocks N," is then a floor).

**Blocked** — most-gated-first (`blocked`, each `number`, `title`, `blockedBy`, `subIssueGate`):

```markdown
- #61 - Title — blocked by open #42, #43
- #70 - Title — blocked by open sub-issue children
```

Render each blocked issue's open `blockedBy` numbers. When `blockedBy` is empty and `subIssueGate` is true, the issue is blocked purely by open sub-issue children — render "blocked by open sub-issue children," not an empty "blocked by open" clause; when both are present, note the sub-issue gate alongside the edges. If `blockedTruncated`, render `blockedCount` as the total; honor per-issue `blockedByTruncated` (a short `blockedBy` under the flag is a floor, not a confirmed "none").

**Provisional** is the truncation-safety class: an issue presenting no open blocker but whose `blockedBy` edge list was capped (`blockedByTruncated`) — it cannot be confirmed ready, so it is neither counted ready nor listed as a gate. Surface the count when non-zero.

This block is repo-wide and convention-free, so a blocked issue that is *also* deferred appears both here and in Deferred Issue Review above — that is expected (dependency-structure lens vs parked-state lens), not duplication to suppress. If `blockedCount`, `provisionalCount`, and `gateCount` are all zero, there is no blocking structure in the fetched window: "No open native dependency edges — the fetched open issues are all independent" (or, when `fetchedCount` is 0, "no open issues in the fetched window").

### Backlog Trajectory

Header: `## Backlog Trajectory`

From the `trajectory` block. **If `available` is false**, the trajectory fetch was degraded — render "Trajectory unavailable: \<`unavailable`\>" and skip the table; do not infer growth/shrinkage. When available, render `windows` (each `days`, `created`, `closed`, `net`):

```markdown
| Window  | Created | Closed | Net |
| ------- | ------- | ------ | --- |
| 7 days  | 2       | 1      | +1  |
| 30 days | 6       | 9      | -3  |
| 90 days | 14      | 11     | +3  |
```

Add a one-line read per the longest window: net positive = growing (new work identified faster than resolved — normal during active development); net zero = stable; net negative = shrinking (good momentum). Note `fetchTruncated` if set (the activity window was capped — treat as directional).

### Critical Path / Gate Status

Header: `## Critical Path / Gate Status`

From the `criticalPath` block. If `configured` is false, the repo declares no critical path — render "No critical-path convention configured for this repo" and move on (the same no-op posture as `deferred.configured == false`). When configured, render `streams` in their declared order — the order *is* the path — each with its `gateCleared` status and its open critical-path `members` (number/title):

```markdown
1. **api-contract** — gate **cleared** (no open critical-path issue)
2. **ingest** — gate **open**: #51 - Title, #58 - Title
```

A stream's gate is **cleared** when no open critical-path issue remains in it (so a downstream stream may begin) and **open** otherwise. The gate is a windowed fact: when `fetchTruncated` is set, mark every gate **provisional** — it is computed before the list cap, so a cleared gate is authoritative only on a complete window. Then surface misplaced critical-path issues: `unareaedCount` (labeled critical-path but carrying no area) and `offPathCount` (in a real area outside the declared path) — both claim the critical path without sitting on it, worth a reviewer's eye. Honor per-stream `listTruncated`.

### Rate-limit note (conditional)

The `rateLimit` block is present only when the GraphQL points budget ran low during the fetch (`omitempty` — it is usually absent). When present, render a short caution: `remaining` points left, resets at `resetAt`. Absent means no concern — say nothing. (A *hard* rate-limit failure surfaces as a tool-call error in Step 1, not as this block.)

## Step 3: Key Findings & discussion

Header: `## Key Findings`

Synthesize 3–5 prioritized, action-oriented findings from the sections above — the items that actually warrant grooming attention, not a recap of every section. Suggested priority order:

1. Stale issues with no engagement (potential backlog rot)
2. Quality gaps — issues with no body or no labels that block triage
3. Gate roots — ready issues that unblock the most open downstream work (high-leverage do-first candidates, from Dependency Structure)
4. Premise/scope questions raised by overlap or cross-reference clusters
5. Area imbalance suggesting misallocated effort
6. Trajectory signal (sustained growth worth watching)

Each finding names a concrete next step. Then close with the read-only transition:

> This is a read-only analysis — no issues were modified. Which area would you like to discuss first?

## Rules

- **Read-only.** Never relabel, close, or modify issues. This skill renders facts; acting on them is a separate, explicit decision.
- **Render every section client-side.** The server returns structured facts only — there is no pre-rendered summary to pass through.
- **Conservative overlap.** Present overlap and cross-reference groups as candidates for human judgment; never declare duplicates.
- **Honor truncation seams.** A truncated block is a lower bound — say so rather than implying completeness.
- **No fabricated depth.** Where overstory doesn't compute a signal (premise validity, milestone partition, active-vs-deferred per area, possibly-resolved-against-closed), name the gap; don't invent the analysis.
````

# Reference Skill: Project Summary

A worked render skill for the `project_summary` tool — the orientation read ("given what's open now, what should I pick up?"). It walks the tool's blocks in order, renders the factual sections straight through, and layers caller-side ranking onto the recommendation inputs. See [Rendering the Facts](../rendering.md) for the concept this skill makes concrete, and [Backlog Review](./backlog-review.md) for its grooming counterpart.

> **Reference skill.** This is the reference render skill for `project_summary`, maintained in this repository. It is an illustrative example, not a fixed interface: the conventions evolve with the server's reductions, so adapt it to your repository. To use it, copy the body below into `~/.claude/skills/pm-project-summary/SKILL.md` (or `~/.cursor/skills/pm-project-summary/SKILL.md` for Cursor) and adjust as needed. Last revised 2026-07.

````markdown
---
name: pm-project-summary
description: >-
  Activate when the user wants a quick orientation on where a GitHub repository
  stands and what to pick up next — trigger phrases include "project summary",
  "what's the project status", "what should I work on", "session orientation",
  "show open issues", or "milestone progress". Renders the overstory MCP server's
  structured orientation facts — active-milestone progress, area inventory,
  hygiene signals, open PRs, and ranked recommendation inputs — into a short
  status report. For quick orientation, not deep grooming (use pm-backlog-review
  for that); for transitioning between planning units — closing or creating
  milestones — use pm-milestone-lifecycle.
---

# Project Summary

Quick session orientation: where the repo stands now and what's worth picking up. The overstory MCP server does the fetching and reduction; this skill renders its structured facts into a short report, then transitions to a decision. It is the orientation counterpart to `pm-backlog-review`'s grooming read — a different stance over the same repo, not a deeper version of it.

The server returns **pure structured facts** — no prose, no markdown. Every section below is rendered client-side from those facts. The skill is generic: it carries no repo's conventions. A repo's specifics (which labels are areas, which mean deferred or bug, its day and body-length thresholds) come from the server's manifest, surfaced in the facts; this skill never hardcodes them.

## Step 1: Call the tool

```text
mcp__overstory__project_summary(owner=<owner>, repo=<repo>, limit=25)
```

Resolve `owner` and `repo`: if the user named a repository, use that `owner/repo` directly. Otherwise resolve from the current directory (requires `gh`):

```bash
gh repo view --json owner,name --jq '"owner=\(.owner.login) repo=\(.name)"'
```

Pass the two resolved values to the tool's `owner` and `repo` parameters.

`limit` caps how many items each block *lists* (milestone members and the milestone list, issues per hygiene signal, open PRs, recommendation candidates). `25` keeps this a scannable orientation read — a low cap, unlike `pm-backlog-review`'s `100` for grooming. Coverage of how many issues are *examined* is governed by the manifest's fetch limits, independent of `limit`. On an active backlog a low cap will list-truncate the busier signals (commonly missing-area) — that is expected; surface it as a lower bound (below) rather than raising the cap reflexively.

### If the tool is unavailable

If `mcp__overstory__project_summary` is not callable, stop and emit a short precondition guard rather than improvising a `gh`-based status. Check, in order:

- Is the `overstory` binary on `PATH`? (`command -v overstory`)
- Is the overstory server registered in the active MCP config for this tool?
- Has the session been restarted since the server was installed or registered? The stdio server spawns at session start — a mid-session install is not picked up until restart.

Report which check failed and stop. This skill has **no `gh` fallback** by design: overstory is the single source of the reductions, and a hand-rolled `gh` read would silently diverge from what the server computes.

## Step 2: Render the report

The tool returns a composite object: `repo`, `generatedAt`, one block per section — `milestones`, `areaInventory`, `hygiene`, `openPRs`, `recommendations`, `criticalPath`, `dependencies` — a top-level `openIssueSet` (consumed by What's Next in Step 3, not rendered as its own section), and an optional `rateLimit`. Render the six factual sections below from their blocks, in order; synthesize What's Next from `recommendations` last (Step 3).

**Truncation is load-bearing.** Blocks carry `fetchTruncated` (the scan window didn't cover every open issue), `listTruncated` (more matches exist than were listed), and — on milestones — `membershipTruncated` (a milestone's listed members are a floor relative to its open count). When any is true, render an explicit "lower bound" note for that section: the result is a floor, not a complete picture. Never present a truncated run as exhaustive. Each `recommendations` candidate additionally carries per-edge dependency flags, and the top-level `openIssueSet` carries `fetchTruncated`; these bound the dependency reasoning in What's Next (Step 3). `blockedByTruncated` / `blockingTruncated` bound the *named* blocker / blocking lists — an empty list under a true flag is a floor, not a confirmed "none." `subIssuesTruncated` bounds only the *named* sub-issue children; the sub-issue **gap** (`subIssuesTotal − subIssuesCompleted`) is an authoritative summary count over all children, **not** bounded by it. Additionally, the response as a whole carries a top-level `sizeBound` marker **only when** it was trimmed to a serialization byte budget — `finalBytes`/`maxBytes` plus a `trimmedBlocks[]` of `{block, dropped, remaining}` (where `block` may be a nested path, e.g. `hygiene.stale`). Each trimmed block also sets its own `listTruncated`, so the per-block lower-bound note still fires; `sizeBound` adds the *cause* (a size trim, not a `limit` cap) and the count dropped. When present, flag the response as size-bounded and note the remedy is a narrower request (fewer `blocks`) or a higher per-repo `maxBytes`, not a higher `limit`.

### Active Milestones

Header: `## Active Milestones`

From the `milestones` block. **If `available` is false**, the milestone fetch was degraded — render "Milestones unavailable: \<`unavailable`\>" using the literal reason (`rate_limited` or `fetch_failed`) and skip the rest of this section; do not infer progress. When available, lead with `openMilestones` (the repo's exact open-milestone count). For each entry in `milestones`, render its authoritative `openIssues`/`closedIssues` counts (read from the milestone object, so they stay exact even when the issue window truncates) and its `members` (the open issues from the fetched window belonging to it, each with `number`, `title`, `ageDays`), oldest-first:

```markdown
**ghostty-theme UX** — 8 open / 0 closed
- #245 - feat(ghostty-theme): auto-reload config after theme selection — age 87d
```

If `membershipTruncated` is set on a milestone, its member list is a floor — fewer members are listed than its open count, because the issue window or the list cap fell short — so note it. If `listTruncated`, more milestones exist than listed. If `openMilestones` is 0: "No open milestones."

### Area Inventory

Header: `## Area Inventory`

From the `areaInventory` block. This is **counts-only**: for each entry in `areas` (`area`, `active`, `deferred`) render the active/deferred split, busiest first, then the `unclassified` (`active`/`deferred`) count. The block carries **no issue numbers** — it answers "where does open work sit," not "which issues." For the issue-level "what's unlabeled," point at the Hygiene missing-area signal below, which does list issues.

```markdown
| Area           | Active | Deferred |
| -------------- | ------ | -------- |
| ghostty        | 8      | 0        |
| ai-skills      | 1      | 0        |
| (unclassified) | 24     | 0        |
```

A multi-area issue counts in each area it matches, so the column needn't sum to the open total. If `fetchTruncated`, note the counts are a lower bound.

### Hygiene Signals

Header: `## Hygiene Signals`

From the `hygiene` block. Four signals over the open issues — `missingArea`, `unmilestonedAged`, `stale`, `deferredWithoutContext` — each carrying a `count`, a capped `issues` list (`number`, `title`, `ageDays`, `inactiveDays`), and `listTruncated`. Render each as its count plus its list (most-inactive first), and render "None" when `count` is 0 so the reader sees the check ran:

```markdown
- **Missing area label** (24): #362 - Title (age 49d); … — _lower bound: 4 more not listed_
- **Unmilestoned & aged** (6): …
- **Stale** (14): …
- **Deferred without context**: None
```

These are not disjoint — one issue can trip several, so the counts need not sum to anything. The thresholds are the repo's manifest conventions (the server applies them; they are not in this skill): "unmilestoned & aged" and "stale" use the repo's age and staleness day thresholds, and "deferred without context" flags a deferred issue whose body falls below the repo's `minBodyLength` (an empty body, on a repo that has not tuned that bar). **Do not name a specific day count or character count** — it is per-repo and lives in the manifest, not here. Note `listTruncated` per signal.

### Open PRs

Header: `## Open PRs`

From the `openPRs` block. **If `available` is false**, render "Open PRs unavailable: \<`unavailable`\>" using the literal reason and skip the rest. When available, lead with `openPRCount` and `stalePRCount`, then list `pullRequests` (each `number`, `title`, `branch`, `draft`, `ciStatus`, `inactiveDays`, `stale`), most-inactive first:

```markdown
- #10 - Title (branch `feature/x`, CI: SUCCESS, ready) — inactive 2d
```

Render `ciStatus` verbatim (e.g. `SUCCESS`, `FAILURE`, `PENDING`); an **empty** `ciStatus` means no checks were reported — render "no checks", distinct from a pending rollup. Mark `draft` vs ready, and flag the `stale` ones. If `openPRCount` is 0: "No open PRs." Note `listTruncated` if set.

### Critical Path / Gate Status

Header: `## Critical Path / Gate Status`

From the `criticalPath` block. If `configured` is false, the repo declares no critical path — render "No critical-path convention configured for this repo" and move on. If `available` is false the repo *is* configured but the critical-path fetch failed — render the block as unavailable (the reason is in `unavailable`, e.g. `rate_limited`/`fetch_failed`), the same degrade posture the milestone and PR blocks take, and never read its empty `streams` as a set of cleared gates. When configured and available, render `streams` in their declared order — the order *is* the path — each with its `gateCleared` status and its open critical-path `members` (number/title):

```markdown
1. **api-contract** — gate **cleared** (no open critical-path issue)
2. **ingest** — gate **open**: #51 - Title, #58 - Title
```

A stream's gate is **cleared** when no open critical-path issue remains in it (so a downstream stream may begin) and **open** otherwise. The block is sourced from the critical-path-labeled subset the gate depends on — the fetched window when it already covers every open issue, otherwise a dedicated label-scoped fetch of that subset — so a cleared gate and the member list are authoritative regardless of backlog size. Only `fetchTruncated` makes a cleared gate **provisional**, and it is rare: it marks the labeled subset *itself* exceeding the fetch cap, not the general backlog exceeding the window. When it is set, mark every gate provisional. Then surface misplaced critical-path issues: `unareaedCount` (labeled critical-path but carrying no area) and `offPathCount` (in a real area outside the declared path). Honor per-stream `listTruncated`.

### Dependency Classification

Header: `## Dependency Classification`

From the `dependencies` block — a graph-level ready/blocked/gate classification over GitHub's **authoritative native** blocked-by/blocking edges (plus sub-issue hierarchy), convention-free. Its "gates" are **native-edge do-first roots** (a ready issue that blocks open downstream work), distinct from the manifest critical-path *stream* gate directly above — two different notions of "gate," so name which one when you render both.

Lead with the ready/blocked/provisional split over `fetchedCount` (the classification partitions the *fetched* window), carrying `openIssueCount` as the repo-wide open total:

```markdown
**Dependencies: 12 ready / 4 blocked / 1 provisional** — 17 of 22 open classified
```

When `fetchTruncated`, the split covers only the fetched window — the `openIssueCount − fetchedCount` remainder is **unclassified** (a coverage floor); never present the split as covering `openIssueCount` (the three counts sum to `fetchedCount`). Each fetched issue's verdict is authoritative — unlike `criticalPath`, whose gate goes blanket-provisional whenever *its* `fetchTruncated` fires (now rare: only when the labeled critical-path subset overflows the cap), this block's per-issue verdicts survive a truncated window. **Provisional** = presents no open blocker but its `blockedBy` edge list was capped, so readiness is unconfirmed.

Then the gate set (`gates`, each `number`, `title`, `blockingCount`), most-leverage-first:

```markdown
- #42 - Title — unblocks 4 open
```

If `gatesTruncated`, render `gateCount` as the authoritative total ("12 gates, showing 10"); honor per-gate `blockingTruncated` (a floored `blockingCount`). If `blockedCount`, `provisionalCount`, and `gateCount` are all zero: "No open native dependency edges."

This block is **classification-only** — the per-issue blocked list and raw `blockedBy`/`blocking` edges live in `recommendations` (consumed by What's Next below), so this section is the graph-level overview, not a per-candidate read. The gate set is the graph-level twin of the leverage signal What's Next **ranks on**: each ready candidate's `blocking` edges promote a do-first gate root within its tier (Step 3).

### Rate-limit note (conditional)

The `rateLimit` block is present only when the GraphQL points budget ran low during the fetch (`omitempty` — it is usually absent). When present, render a short caution: `remaining` points left, resets at `resetAt`. Absent means no concern — say nothing. (A *hard* rate-limit failure surfaces as a tool-call error in Step 1, not as this block.)

## Step 3: What's Next

Header: `## What's Next`

From the `recommendations` block (its `candidates`). The server supplies per-issue candidate inputs and a neutral pre-sort; **the ranking judgment is this skill's** — the server reduces, the caller ranks. Each candidate carries `number`, `title`, `isBug`, `milestone` (the milestone *title*, or absent — there is no open/closed join, so a present `milestone` tells you only that the issue has one), `ageDays`, `inactiveDays`, and four dependency signals:

- **`blockedBy`** (+ `blockedByTruncated`) — GitHub's **authoritative native blocked-by edges**, already filtered to *open* blockers. A non-empty `blockedBy` is a confirmed gate; no parsing, no resolution lookup needed.
- **`blocking`** (+ `blockingTruncated`) — the reverse edges: open issues this one gates. High `blocking` is leverage — clearing this candidate frees others.
- **`subIssues`** (+ `subIssuesTruncated`, `subIssuesTotal`, `subIssuesCompleted`) — native sub-issue children. The gate is the **gap**: `subIssuesTotal − subIssuesCompleted > 0` means open children remain — gating the parent **even when `subIssues[]` is empty** (children that are cross-repo or beyond the window don't list but still gate). `subIssues[]` names the known-open children; the gap is what gates.
- **`bodyRefs`** — the distinct `#N` references parsed from the issue body — its *stated* dependencies (a heuristic floor, not native edges). Parsed from rendered plaintext, so a `#N` in a code fence won't appear; empty `bodyRefs` is not proof of no dependency. Resolve each against the top-level **`openIssueSet.numbers`**: a ref **∈ numbers** is a confirmed still-open issue in this repo (a live blocker **for a candidate with no non-truncated `blockedBy` edges**; when the candidate carries non-truncated native `blockedBy` edges, those supersede and a non-edge ref is demoted — see the gate rule below); a ref **∉ numbers** is **unresolved/indeterminate** — it may be a closed issue, an open PR (PRs share the number space; `openIssueSet` is issues-only), a cross-repo reference, or — under `openIssueSet.fetchTruncated` — an open issue the window missed. **Never read ∉ numbers as "resolved" or as "ready."**

Rank 3–5 concrete next steps over these inputs, in priority order:

1. **Bugs** (`isBug` true) — friction that compounds.
2. **Active-milestone work** (`milestone` present) — the current planning unit has open work.
3. **Aged backlog** (high `ageDays`, no `milestone`) — candidates for the next planning unit.

**Sequence ready work ahead of gated work — within each tier.** After assigning tiers, re-sequence inside each tier by gate status, read off the dependency signals (authoritative edges first, the `bodyRefs` heuristic only to supplement):

- **Confirmed gated** — any of: a non-empty open `blockedBy`; a positive sub-issue gap (`subIssuesTotal − subIssuesCompleted > 0`, gating even when `subIssues[]` is empty); or a `bodyRef` ∈ `openIssueSet.numbers` on a candidate with **no trustworthy-complete edge set** (`blockedBy` empty, **or** `blockedByTruncated`). Present the candidate as *gated*, name its open blocker(s)/children, and rank it below ready peers in the same tier — so an aged capstone gated on open work no longer floats up on `ageDays` alone. **When `blockedBy` is non-empty *and* `!blockedByTruncated`, it is the authoritative open-blocker set — complete over recorded native edges** (with any sub-issue gap): a `bodyRef` not already in `blockedBy` is demoted to *indeterminate* (next bullet) — surfaced for verification, never named as a blocker — so an issue whose real dependencies are recorded as native edges is not mis-gated by a disclaimed prose `#N` ("no dependency on #N"). Under `blockedByTruncated` the edge list is a declared floor: a non-edge `bodyRef` ∈ numbers may be a truncated-out blocker, so keep it gating and named — do not demote.
- **Confirmed ready** — *all* seams clear: `blockedBy` empty **and** `!blockedByTruncated`; the sub-issue gap zero (`subIssuesTotal − subIssuesCompleted == 0` — authoritative regardless of `subIssuesTruncated`, which bounds only the *named* children); and no `bodyRef` lands ∈ `openIssueSet.numbers`. Rank these first within the tier.
- **Indeterminate** — between the two: a candidate carrying only `bodyRefs` that are **∉ `openIssueSet.numbers`** (not confirmed-open — possibly a closed issue, an open PR, a cross-repo ref, or, under `openIssueSet.fetchTruncated`, an open issue the window missed), or an empty edge list sitting under a true truncation flag. **Not** confirmed-gated (don't down-rank it as blocked) and **not** confirmed-ready (don't promote it) — surface the unresolved refs and order it after ready peers but ahead of confirmed-gated ones (a gated candidate is known-blocked; an indeterminate one only might be). On a candidate with non-empty `blockedBy` **and** `!blockedByTruncated`, its non-edge `bodyRefs` ∈ `openIssueSet.numbers` get the same treatment at the *ref* level: surfaced for verification alongside the candidate's edge-based gate, never counted as blockers (under `blockedByTruncated` they stay named blockers — see **Confirmed gated**).

**Within the confirmed-ready band, sequence by leverage.** A confirmed-ready candidate with a high `blocking` count is a do-first gate root — clearing it unblocks the most open downstream work — so rank it ahead of ready peers with little or no leverage, even when an aged peer is older. This is the per-candidate reflection of the `dependencies.gates` set (Step 2's Dependency Classification, ranked most-leverage-first): a ready candidate that also appears in `gates` is the clearest promote. Honor the truncation seam — a `blocking` count under `blockingTruncated` is a floor (true leverage is at least that), so promote the candidate on its floor (render "unblocks ≥N") above ready peers with lower or no leverage. The one caution: don't let a floored count leapfrog a peer whose *confirmed* (`!blockingTruncated`) `blocking` count already meets or exceeds that floor — there the truncated candidate's true leverage is genuinely unknown, so keep the confirmed-higher peer above rather than promote on an uncertain count. Leverage orders only *within* the ready band of a tier: it never lifts a ready candidate across a tier boundary, and never touches the gated/indeterminate ordering above.

When two candidates gate **each other** — mutual `blockedBy`, or mutual in-`openIssueSet.numbers` `bodyRefs` (a dependency cycle, common for sibling RFCs; the `bodyRefs` path applies to **edgeless** candidates, since an edged candidate's non-edge refs are demoted) — present them as a coupled gated cluster ranked as a unit below ready peers, rather than forcing a strict order between them. (The server already excludes the issue's own number and PR refs from `bodyRefs`, so the skill needs no self/PR filtering.) This re-sequencing is **independent of** the `listTruncated` note below — that flags the candidate *pool* was capped; the dependency seams flag *specific* gates. Both can fire.

```markdown
1. **#58 - Add ingest retry** (bug) — ready: no blockers, no open sub-issues, no live stated deps.
2. **#40 - Ingest schema** (aged backlog) — ready and high-leverage: `blocking` unblocks 3 open (#61, #71, #72); the do-first gate root, so it leads the ready aged work.
3. **#33 - Config loader cleanup** (aged backlog) — ready but low-leverage (unblocks nothing); ranked below #40 despite being older — leverage orders the ready band.
4. **#47 - Refactor auth flow** (aged backlog) — stated dep #12 is *unresolved* (∉ openIssueSet.numbers — a closed issue, an open PR, or a cross-repo ref); indeterminate, not confirmed ready — verify #12 before starting.
5. **#72 - Ingest dashboard** (aged backlog) — *gated*: native `blockedBy` #40 (open, not truncated). Stated refs #18, #19 resolve **∈ openIssueSet.numbers** (confirmed-open) but are disclaimed prose mentions, not edges — demoted to verify-refs, not blockers, because the non-truncated `blockedBy` set is authoritative. Resume after #40.
```

Every recommendation names specific issue numbers — no meta-process suggestions. If `listTruncated`, note the candidate pool was capped. Then close with the read-only transition:

> This is a read-only orientation — no issues were modified. Which would you like to start on?

## Rules

- **Read-only.** Never relabel, close, or modify issues. This skill renders facts and ranks them; acting is a separate, explicit decision.
- **Render every section client-side.** The server returns structured facts only — there is no pre-rendered summary to pass through.
- **Caller owns the ranking.** The server pre-sorts neutrally; the "what's next" priority is this skill's judgment applied over the candidate inputs — not a server verdict.
- **Honor truncation seams.** A truncated block — or a `membershipTruncated` milestone — is a lower bound; say so rather than implying completeness.
- **No hardcoded conventions.** Thresholds, area labels, and deferred/bug labels are the repo's manifest conventions surfaced in the facts; never name a specific day or character count in this skill.
- **Orientation, not grooming.** This is the quick "where are we / what next" read; for deep backlog health (staleness bands, overlaps, cross-references, trajectory) use `pm-backlog-review`.
````

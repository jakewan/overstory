# Reference Skill: Project Summary

A worked render skill for the `project_summary` tool — the orientation read ("given what's open now, what should I pick up?"). It walks the tool's blocks in order, renders the factual sections straight through, and layers caller-side ranking onto the recommendation inputs. See [Rendering the Facts](../rendering.md) for the concept this skill makes concrete, and [Backlog Review](./backlog-review.md) for its grooming counterpart.

> **Provenance.** This is a snapshot as of 2026-06, reproduced from the maintainer's own agent configuration. It is an illustrative example, not a maintained contract: the live conventions evolve with the server's reductions, so adapt it to your repository rather than treating it as a fixed interface. To use it, copy the body below into `~/.claude/skills/pm-project-summary/SKILL.md` (or `~/.cursor/skills/pm-project-summary/SKILL.md` for Cursor) and adjust as needed.

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

The tool returns a composite object: `repo`, `generatedAt`, and one block per section — `milestones`, `areaInventory`, `hygiene`, `openPRs`, `recommendations`, `criticalPath`, `openIssueSet`, and an optional `rateLimit`. Render the five factual sections below from their blocks, in order; synthesize What's Next from `recommendations` last (Step 3). `openIssueSet` has no section of its own — it is the open-issue lookup that Step 3 resolves each candidate's stated dependencies against.

**Truncation is load-bearing.** Blocks carry `fetchTruncated` (the scan window didn't cover every open issue), `listTruncated` (more matches exist than were listed), and — on milestones — `membershipTruncated` (a milestone's listed members are a floor relative to its open count). When any is true, render an explicit "lower bound" note for that section: the result is a floor, not a complete picture. Never present a truncated run as exhaustive.

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

From the `criticalPath` block. If `configured` is false, the repo declares no critical path — render "No critical-path convention configured for this repo" and move on (the same no-op posture the milestone and PR blocks take when degraded). When configured, render `streams` in their declared order — the order *is* the path — each with its `gateCleared` status and its open critical-path `members` (number/title):

```markdown
1. **api-contract** — gate **cleared** (no open critical-path issue)
2. **ingest** — gate **open**: #51 - Title, #58 - Title
```

A stream's gate is **cleared** when no open critical-path issue remains in it (so a downstream stream may begin) and **open** otherwise. The gate is a windowed fact: when `fetchTruncated` is set, mark every gate **provisional** — it is computed before the list cap, so a cleared gate is authoritative only on a complete window. Then surface misplaced critical-path issues: `unareaedCount` (labeled critical-path but carrying no area) and `offPathCount` (in a real area outside the declared path). Honor per-stream `listTruncated`.

### Rate-limit note (conditional)

The `rateLimit` block is present only when the GraphQL points budget ran low during the fetch (`omitempty` — it is usually absent). When present, render a short caution: `remaining` points left, resets at `resetAt`. Absent means no concern — say nothing. (A *hard* rate-limit failure surfaces as a tool-call error in Step 1, not as this block.)

## Step 3: What's Next

Header: `## What's Next`

From the `recommendations` block. The server supplies per-issue candidate inputs and a neutral pre-sort; **the ranking judgment is this skill's** — the server reduces, the caller ranks. Each candidate carries `number`, `title`, `isBug`, `milestone` (the milestone *title*, or absent — there is no open/closed join, so a present `milestone` tells you only that the issue has one), `bodyRefs` (its stated `#N` dependencies, ascending — PR references and the issue's own number excluded), `ageDays`, and `inactiveDays`. Rank 3–5 concrete next steps over these inputs, in priority order:

1. **Bugs** (`isBug` true) — friction that compounds.
2. **Active-milestone work** (`milestone` present) — the current planning unit has open work.
3. **Aged backlog** (high `ageDays`, no `milestone`) — candidates for the next planning unit.

**Ready before gated — a cross-cutting demotion, not a fourth tier.** Within whatever tier a candidate lands in, demote it when it is gated behind unfinished work. Resolve each candidate's `bodyRefs` against `openIssueSet.numbers`:

- **Ref ∈ `openIssueSet.numbers`** — the ref names a live open issue in this repo, so treat the candidate as **gated**: rank it *after* ready work in its tier and name the open gate. The relationship is a candidate to verify, not a proven blocker — a stated `#N` can be "related to" or "supersedes", not "blocked by" — so present it as "gated behind open #N", not "blocked".
- **Ref ∉ `numbers`** — **no live open-issue gate detected here; do not promote the candidate to "ready" on this basis.** Absence is not resolution: the ref may be a closed issue (resolved), an open PR (PRs share the number space and are not in this set), a cross-repo reference (a bare number), or — when `openIssueSet.fetchTruncated` — an open issue beyond the fetched window. Verify by hand before treating it as clear.

So a freshly-aged capstone whose `bodyRefs` include an open sibling ranks below a same-tier candidate whose refs are all absent (or which states none) — ready work first. For example, given `openIssueSet.numbers` that contains `51`, an aged capstone `#58` whose `bodyRefs` are `[51]`, and a same-tier `#72` that states no dependency:

```markdown
- #72 - refactor: extract the parser — ready (states no open-issue dependency)
- #58 - feat: end-to-end synthesis — **gated**: stated dep #51 is a live open issue (verify the relationship)
```

`#72` leads despite `#58` being older, because `#58`'s stated dep `#51` is in the open-issue set and `#72`'s isn't gated at all. Keep the three-tier structure above intact; gating only reorders *within* a tier.

Every recommendation names specific issue numbers — no meta-process suggestions. If `listTruncated`, note the candidate pool was capped (and the `openIssueSet` lookup may also be a floor when `fetchTruncated`). Then close with the read-only transition:

> This is a read-only orientation — no issues were modified. Which would you like to start on?

## Rules

- **Read-only.** Never relabel, close, or modify issues. This skill renders facts and ranks them; acting is a separate, explicit decision.
- **Render every section client-side.** The server returns structured facts only — there is no pre-rendered summary to pass through.
- **Caller owns the ranking.** The server pre-sorts neutrally; the "what's next" priority is this skill's judgment applied over the candidate inputs — not a server verdict.
- **Honor truncation seams.** A truncated block — or a `membershipTruncated` milestone — is a lower bound; say so rather than implying completeness.
- **No hardcoded conventions.** Thresholds, area labels, and deferred/bug labels are the repo's manifest conventions surfaced in the facts; never name a specific day or character count in this skill.
- **Orientation, not grooming.** This is the quick "where are we / what next" read; for deep backlog health (staleness bands, overlaps, cross-references, trajectory) use `pm-backlog-review`.
````

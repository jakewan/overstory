# Tools & Facts

Overstory exposes a set of read-only tools. Each returns a composite of **structured facts** — no prose, no markdown, no pre-rendered output. Turning facts into a report is the caller's job; this separation is what lets one server serve many render styles.

This page documents the *shape and semantics* of what the tools return — the top-level composite, what each block is for, and the cross-cutting conventions. For the exhaustive field-by-field listing, read the Go structs named below: their `json:"..."` tags **are** the wire contract, so pointing at them keeps this reference from drifting as fields are added.

## Common parameters

The manifest-driven reads — `backlog_review`, `project_summary`, and `milestone_tracks` — take the same inputs. (The author- and window-driven reads document their parameters in their own sections — [`authored_activity`](#authored_activity), [`authored_activity_batch`](#authored_activity_batch), [`maintenance_activity`](#maintenance_activity), and [`maintenance_activity_batch`](#maintenance_activity_batch).)

| Parameter | Type    | Required | Default | Bounds  | Meaning                          |
| --------- | ------- | -------- | ------- | ------- | -------------------------------- |
| `owner`   | string  | yes      | —       | —       | Repository owner (user or org).  |
| `repo`    | string  | yes      | —       | —       | Repository name.                 |
| `limit`   | integer | no       | `20`    | `1`–`100` | Max items listed per reduction.  |

Repo targeting is explicit — there is no ambient default repository. The conventions applied come from the manifest entry for `owner/repo` (see [Manifests](./manifest.md)).

`limit` caps how many items each block *lists*; it does **not** govern how many issues are *examined* — that's the manifest's per-reduction fetch limits, independent of `limit`. A list capped by `limit` sets its `listTruncated` flag (below).

## Cross-cutting conventions

These hold across every block of both composites:

- **Hoisted identity.** Each composite carries `repo` and `generatedAt` at the top level.
- **Truncation is explicit, never silent.** A caller must be able to tell incomplete data from complete data. Blocks carry:
  - `fetchTruncated` — the scan window didn't cover every open issue (counts are a lower bound).
  - `listTruncated` — more matches exist than were listed under `limit`.
  - `membershipTruncated` (milestones) — a milestone's listed members are a floor relative to its open count.
  - `refsTruncated` (cross-reference) — not all references were retrieved.
- **Degradation is per-block, not fatal.** Blocks needing their own fetch (`trajectory` in `backlog_review`; `milestones` and `openPRs` in `project_summary`; the whole of `milestone_tracks`, which is a single milestone-fetch reduction) carry `available`; when a fetch fails they set `available: false` and an `unavailable` reason (`rate_limited` or `fetch_failed`) instead of failing the whole call. A *hard* rate-limit failure on a tool's **primary** fetch — the open-issue fetch `backlog_review` and `project_summary` lead with — surfaces as a tool-call error rather than a degraded block. `milestone_tracks` has no primary fetch: its single milestone fetch degrades like the blocks above, so it never fails the call on a rate limit.
- **Total-size bound (`sizeBound`).** On a large repository the assembled composite could exceed the MCP client's tool-result token cap and fail the call. To prevent that, each composite trims its detail item-lists to fit the [`response.maxBytes`](./manifest.md#response) budget — flat lists by item, overlap/cross-reference by whole group, milestones by whole milestone, balanced largest-contributor-first so no single block is gutted before the others. Counts, `openIssueSet`, the critical-path gate signal, and summary fields are never trimmed; a trimmed block sets its `listTruncated`, so under a bound a block's `limit` no longer predicts its listed count. A bounded response carries a top-level `sizeBound` (`maxBytes`, `finalBytes`, and per-block `{ block, dropped, remaining }`); `finalBytes` is one serialization (the wire carries roughly twice that) and is an upper bound, so when even the irreducible content exceeds the budget it reports the overflow rather than falsely claiming success. Struct: `reduce.SizeBoundFacts` in `internal/reduce/`.
- **`omitempty` fields.** `rateLimit` and `sizeBound` (top level) appear only when relevant — the GraphQL points budget ran low, or the response had to be trimmed; `unavailable` appears only on a degraded block; a recommendation candidate's `milestone` is absent when the issue is unmilestoned.

## `backlog_review`

The **grooming** read: what in the backlog needs maintenance attention. Composite struct: `backlog.Facts` in `internal/backlog/`.

| Block         | Answers                                                                 | Struct (in `internal/backlog/`) |
| ------------- | ---------------------------------------------------------------------- | ------------------------------- |
| `staleness`   | How much of the backlog is *neglected* — inactive and not deliberately parked — in bands, with the stalest issues. Issues carrying a deferred label are excluded (quiet by design, not neglected) and reported separately as a count. `thresholdSource` reports whether the threshold came from the manifest (`"manifest"`) or the generic default (`"default"`). | `staleness.go` |
| `deferred`    | Open issues carrying the manifest's deferred labels (reports not-configured when none declared). | `deferred.go` |
| `areaBalance` | Issue distribution across functional areas, plus unclassified and multi-area counts. | `area.go` |
| `quality`     | Open issues with a too-thin body, no labels, or a missing required category. | `quality.go` |
| `overlap`     | Groups of open issues with similar titles — candidate duplicates.       | `overlap.go` |
| `crossRef`    | Groups of open issues that reference one another — candidate consolidation. | `crossref.go` |
| `trajectory`  | Per lookback window, issues created, closed, and net — the growing/shrinking signal. Aggregate (unaffected by `limit`); degradable. | `trajectory.go` |
| `criticalPath`| When the manifest declares a critical path: each declared stream in order, its open critical-path issue members, and a per-stream `gateCleared` signal (provisional under `fetchTruncated`); off-path/unareaed counts for misplaced issues. Not configured ⇒ `configured: false` no-op. | `criticalpath.Facts` (in `internal/criticalpath/`) |
| `openIssueSet` | The ascending, distinct open issue `numbers` in the fetched window — the surface a caller resolves a deferred issue's `bodyRefs` against. Same-repo, open, issues-only; the full window, never `limit`-capped (`fetchTruncated` marks a floor). Presence names a live open issue; absence is not proof of resolution. | `reduce.OpenIssueSetFacts` (in `internal/reduce/`) |

Plus the optional top-level `rateLimit` and `sizeBound`.

## `project_summary`

The **orientation** read: given what's open now, what to pick up. Composite struct: `summary.Facts` in `internal/summary/`.

| Block             | Answers                                                                       | Struct (in `internal/summary/`) |
| ----------------- | ---------------------------------------------------------------------------- | ------------------------------- |
| `milestones`      | Each open milestone's authoritative open/closed counts and its fetched open members. Degradable; per-milestone `membershipTruncated`. | `milestones.go` |
| `areaInventory`   | Per area, the active-vs-deferred split of open issues (counts only, no issue numbers), plus unclassified. | `area.go` |
| `hygiene`         | Four signals over open issues: missing-area, unmilestoned-and-aged, stale (neglected — deferred issues excluded), deferred-without-context. | `hygiene.go` |
| `openPRs`         | Each open PR's branch, draft/ready state, CI rollup, and inactivity, plus a stale-PR count. Degradable. | `pullrequests.go` |
| `recommendations` | Per-issue inputs (bug-labeled, milestone, age, inactivity) a caller ranks "what next" from. The ranking judgment stays caller-side. | `recommendations.go` |
| `criticalPath`    | When the manifest declares a critical path: each declared stream in order, its open critical-path issue members, and a per-stream `gateCleared` signal (provisional under `fetchTruncated`); off-path/unareaed counts for misplaced issues. Not configured ⇒ `configured: false` no-op. | `criticalpath.Facts` (in `internal/criticalpath/`) |
| `openIssueSet`    | The ascending, distinct open issue `numbers` in the fetched window — the surface a caller resolves a recommendation candidate's `bodyRefs` against, so an age-driven ranking can demote a candidate gated behind an open sibling. Same-repo, open, issues-only; the full window, never `limit`-capped (`fetchTruncated` marks a floor). Presence names a live open issue; absence is not proof of resolution. | `reduce.OpenIssueSetFacts` (in `internal/reduce/`) |

Plus the optional top-level `rateLimit` and `sizeBound`.

> **The server reduces; the caller ranks and renders.** `recommendations` supplies neutral per-issue inputs, not a verdict — ordering them into "what to do next" is the caller's judgment. Likewise every block returns facts, never narrative.

## `milestone_tracks`

The within-milestone **priority-structure** read: the ordered tracks operators encode in a milestone's *description* — distinct from `project_summary`'s milestone *progress* (counts and members). Composite struct: `summary.MilestoneTracksFacts` in `internal/summary/milestonetracks.go`.

For each open milestone, the parsed `tracks` in description order, each carrying a `label`, an optional raw `status` (a bold run-in's parenthetical, uninterpreted), and ordered `members` — each an issue `number` with a raw `statusToken` (`~~` for a struck/abandoned member, a checkbox marker char, or absent). Tracks are recognized by manifest-declared markers (heading levels and/or bold run-in labels) with a prose-section label stoplist; see [`milestoneTracks`](./manifest.md#milestonetracks). A description with no track structure yields a milestone with no tracks — the common case — not an error. The block is a single milestone fetch (which also retrieves each milestone's raw-markdown description), degradable as above; the milestone-list, per-milestone track-list, and per-track member-list truncation seams are each surfaced.

> **Structural extraction, caller-side judgment.** The server emits the structure it parses; deciding which track is "on top" or where a cut line falls is the caller's judgment. **Member precision:** list-structured tracks (checkbox/numbered) extract cleanly, but references in inline-prose tracks, markdown link text, or HTML blocks are captured verbatim and may be dependency *mentions* rather than members (pull-request references are excluded; a bare `#N` that is actually a PR cannot be distinguished from an issue without a live lookup). This is an accepted imprecision of structural extraction — the caller filters, and a future live-issue-state join is the real resolver.

## `authored_activity`

The **attention** read: how much one user authored and engaged with in a repository over a bounded window — the per-repo measure primitive a cross-project attention audit loops over. Composite struct: `authored.Facts` in `internal/authored/`.

Unlike the manifest-driven reads it is **author- and window-driven and reads no manifest conventions**, and takes its own parameters:

| Parameter | Type   | Required | Default | Meaning                                                        |
| --------- | ------ | -------- | ------- | ------------------------------------------------------------- |
| `owner`   | string | yes      | —       | Repository owner (user or org).                               |
| `repo`    | string | yes      | —       | Repository name.                                              |
| `author`  | string | yes      | —       | GitHub login whose authored and engagement activity is measured. |
| `since`   | string | yes      | —       | Window start, an RFC3339 timestamp.                           |
| `until`   | string | no       | now     | Window end, an RFC3339 timestamp.                             |

It returns six **decomposed counts** under `counts` — `commitsAuthored`, `issuesOpened`, `pullRequestsOpened`, `reviewsSubmitted` (others' PRs), `pullRequestsEngaged` (commented, not authored), and `issuesEngaged` — each a `{ count, fidelity }` pair, plus the echoed `author`/`since`/`until` and the optional top-level `rateLimit`. The counts are never summed; weighting and the attention verdict stay caller-side.

There are **no list/fetch truncation seams** here (these are counts, not bounded lists) and degradation is **all-or-nothing**: any fetch failure surfaces as a tool-call error (a throttle names its retry instant), and an unresolved `author` login is a named error rather than six zeros — a silently-partial count would understate attention. Because it inherits the operator's `gh` credentials, it can measure private repositories the user-rooted contributions query cannot reach.

> **Per-category fidelity is part of the contract.** The categories are not equally precise, and each `count` carries a `fidelity` label saying so: `commitsAuthored` is the default-branch commit count attributed to the author's linked identity (it misses squash-merged and email-unlinked commits), while the search-derived counts are search-index-approximate and — for reviews and engagement — windowed by the item's activity rather than the exact comment/review date. A caller reads each count through its label rather than as uniform ground truth.

## `authored_activity_batch`

The **batched attention** read: the same per-user measure as [`authored_activity`](#authored_activity), fanned out across several repositories in one call — the shape a cross-project attention audit reaches for directly instead of looping single-repo calls. Composite struct: `authored.BatchFacts` in `internal/authored/`.

Like `authored_activity` it is **author- and window-driven and reads no manifest conventions**, but takes a list of repositories instead of one:

| Parameter | Type     | Required | Default | Bounds | Meaning                                                  |
| --------- | -------- | -------- | ------- | ------ | -------------------------------------------------------- |
| `repos`   | string[] | yes      | —       | 1–50   | The repositories to measure, each an `owner/repo` slug.  |
| `author`  | string   | yes      | —       | —      | GitHub login whose activity is measured.                 |
| `since`   | string   | yes      | —       | —      | Window start, an RFC3339 timestamp.                      |
| `until`   | string   | no       | now     | —      | Window end, an RFC3339 timestamp.                        |

It returns one `RepoActivity` entry per repository under `repos`, in request order, each either **available** with the same six `{ count, fidelity }` counts as `authored_activity`, or **unavailable** with a reason (`not_found`, `rate_limited`, `fetch_failed`, `not_attempted`) and — for a throttle — the `resetAt` instant. The batch echoes `author`/`since`/`until` once and carries a single aggregated top-level `rateLimit`.

**Degradation is per repository, not all-or-nothing** — the one contract difference from the single-repo tool. A repository that is missing or failed degrades only its own entry while the rest return their counts (a throttle is the exception — it trips batch-wide backpressure, below). The aggregated `rateLimit` is the tightest remaining across the successful repositories, or — when any was throttled — `{ remaining: 0, resetAt }` at the earliest throttle reset, so a caller is never told it has budget mid-throttle. Two conditions still fail the whole call: a pre-fetch validation error (an empty or oversized `repos` list, a malformed or duplicate slug, an unparseable or inverted window), and an **unresolvable** `author` login — repo-independent, so it fails every repository identically and surfaces as one named error rather than N markers.

**The fan-out adapts to two adverse conditions under load.** On a `rate_limited` signal it stops launching new fetches rather than amplifying the throttle, so an arbitrary subset of the not-yet-started repositories returns `not_attempted` (a deliberate skip, not a failure — not the request-order tail). Because the author login rides the same request that can be throttled, a throttle that precedes the author's resolution can pre-empt the whole-batch author error above: the batch then returns a throttle plus `not_attempted` markers, and the unresolvable login surfaces cleanly on retry after `resetAt`. Independently, each repository's fetch carries its own deadline, so one hung repository degrades to `fetch_failed` without stalling the batch.

> **A resolved-but-wrong login yields honest zeros.** The whole-batch author error catches only an *unresolvable* login; a login that resolves to a real but unrelated account returns genuine-looking zeros that no tool can distinguish from real inactivity — the same limit the single-repo tool carries.

## `maintenance_activity`

The **maintenance-attention** read: the state mutations one user paid to existing issues and pull requests in a repository over a bounded window — the relabeling, milestoning/demilestoning, deferral-labeling, closing/reopening, assigning, and renaming that the authored counts structurally miss (a grooming afternoon produces near-zero authored counts but real maintenance attention). Composite struct: `maintenance.Facts` in `internal/maintenance/`.

Like `authored_activity` it is **author- and window-driven and reads no manifest conventions**, and takes the same parameters ([`owner`, `repo`, `author`, `since`, `until`](#authored_activity)). It is the project's **first REST-sourced read** — the GitHub issue-events stream has no GraphQL equivalent — which shapes the contract differences below.

It returns the touched issues and PRs under `items`, **most-recently-touched first**, each carrying `isPullRequest` and the actor's qualifying mutations in chronological order; each event carries its `type`, instant, per-type payload (label name, milestone title, assignee login, or rename before/after), and a `viaAutomation` flag. The `truncated` flag marks a window the fetch could not fully cover; the echoed `author`/`since`/`until` and the optional top-level `rateLimit` round out the facts. The server stays tag-blind — splitting the issue/PR mix, weighting, and the attention verdict stay caller-side.

The REST-shaped differences from `authored_activity`:

- **An unknown actor yields zero items, not an error.** The actor is matched by login string against the events stream — there is no resolution step — so an unknown or inactive login simply produces an empty `items` list, the opposite of `authored_activity`'s named author-not-found error.
- **The `rateLimit` budget is the REST core pool** (requests per hour), a **different pool** from the authored reads' GraphQL points. The two budgets are not comparable and must never be combined.
- **A far-past `until` over-reads.** The fetch scans back from now to the `since` floor; `until` is applied afterward. A window ending near now (the usual case) costs nothing extra, but a window ending far in the past reads everything newer than `until` and discards it, and can report `truncated` with no in-window items.

> **`viaAutomation` carries the meaning, not the count.** The flag is set when GitHub attributes an event to a GitHub App, so a caller can exclude workflow/app-driven churn — but an automation running *as* the measured login is still attributed to that login, so a consumer reads each event through its flag rather than trusting the raw item count as purely human attention.

## `maintenance_activity_batch`

The **batched maintenance-attention** read: the same per-user measure as [`maintenance_activity`](#maintenance_activity), fanned out across several repositories in one call. Composite struct: `maintenance.BatchFacts` in `internal/maintenance/`. It takes the same list-of-repositories parameters as [`authored_activity_batch`](#authored_activity_batch) (`repos` 1–50, `author`, `since`, `until`).

It returns one `RepoActivity` entry per repository under `repos`, in request order, each either **available** with the same grouped `items` as the single-repo tool, or **unavailable** with a reason (`not_found`, `rate_limited`, `fetch_failed`, `not_attempted`) and — for a throttle — the `resetAt` instant. The batch echoes `author`/`since`/`until` once and carries a single aggregated top-level `rateLimit` (the REST core pool — not comparable with `authored_activity_batch`'s GraphQL points).

**Degradation is per repository, and the fan-out adapts under load exactly as [`authored_activity_batch`](#authored_activity_batch) does** — a missing or failed repository degrades only its own entry; a `rate_limited` repository trips batch-wide backpressure so an arbitrary subset of the not-yet-started repositories returns `not_attempted`; each repository's fetch carries its own deadline. The aggregated `rateLimit` is the tightest remaining across the successful repositories, or `{ remaining: 0, resetAt }` at the earliest throttle reset. Pre-fetch validation (an empty or oversized `repos` list, a malformed or duplicate slug, an unparseable or inverted window) still fails the whole call.

The **one contract difference from `authored_activity_batch`**: there is **no whole-batch author error**. The actor is a stream filter, not a resolved identity, so an unknown login yields zero items per repository rather than failing the batch — and a throttle therefore never pre-empts an author error here, because there is none to pre-empt.

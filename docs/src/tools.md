# Tools & Facts

Overstory exposes five read-only tools. Each returns a composite of **structured facts** — no prose, no markdown, no pre-rendered output. Turning facts into a report is the caller's job; this separation is what lets one server serve many render styles.

This page documents the *shape and semantics* of what the tools return — the top-level composite, what each block is for, and the cross-cutting conventions. For the exhaustive field-by-field listing, read the Go structs named below: their `json:"..."` tags **are** the wire contract, so pointing at them keeps this reference from drifting as fields are added.

## Common parameters

The three manifest-driven reads — `backlog_review`, `project_summary`, and `milestone_tracks` — take the same inputs. (`authored_activity` and `authored_activity_batch` are author- and window-driven; their parameters are documented in their own sections — [`authored_activity`](#authored_activity) and [`authored_activity_batch`](#authored_activity_batch).)

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
- **`omitempty` fields.** `rateLimit` (top level) appears only when the GraphQL points budget ran low; `unavailable` appears only on a degraded block; a recommendation candidate's `milestone` is absent when the issue is unmilestoned.

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

Plus the optional top-level `rateLimit`.

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

Plus the optional top-level `rateLimit`.

> **The server reduces; the caller ranks and renders.** `recommendations` supplies neutral per-issue inputs, not a verdict — ordering them into "what to do next" is the caller's judgment. Likewise every block returns facts, never narrative.

## `milestone_tracks`

The within-milestone **priority-structure** read: the ordered tracks operators encode in a milestone's *description* — distinct from `project_summary`'s milestone *progress* (counts and members). Composite struct: `summary.MilestoneTracksFacts` in `internal/summary/milestonetracks.go`.

For each open milestone, the parsed `tracks` in description order, each carrying a `label`, an optional raw `status` (a bold run-in's parenthetical, uninterpreted), and ordered `members` — each an issue `number` with a raw `statusToken` (`~~` for a struck/abandoned member, a checkbox marker char, or absent). Tracks are recognized by manifest-declared markers (heading levels and/or bold run-in labels) with a prose-section label stoplist; see [`milestoneTracks`](./manifest.md#milestonetracks). A description with no track structure yields a milestone with no tracks — the common case — not an error. The block is a single milestone fetch (which also retrieves each milestone's raw-markdown description), degradable as above; the milestone-list, per-milestone track-list, and per-track member-list truncation seams are each surfaced.

> **Structural extraction, caller-side judgment.** The server emits the structure it parses; deciding which track is "on top" or where a cut line falls is the caller's judgment. **Member precision:** list-structured tracks (checkbox/numbered) extract cleanly, but references in inline-prose tracks, markdown link text, or HTML blocks are captured verbatim and may be dependency *mentions* rather than members (pull-request references are excluded; a bare `#N` that is actually a PR cannot be distinguished from an issue without a live lookup). This is an accepted imprecision of structural extraction — the caller filters, and a future live-issue-state join is the real resolver.

## `authored_activity`

The **attention** read: how much one user authored and engaged with in a repository over a bounded window — the per-repo measure primitive a cross-project attention audit loops over. Composite struct: `authored.Facts` in `internal/authored/`.

Unlike the three reads above it is **author- and window-driven and reads no manifest conventions**, and takes its own parameters:

| Parameter | Type   | Required | Default | Meaning                                                        |
| --------- | ------ | -------- | ------- | ------------------------------------------------------------- |
| `owner`   | string | yes      | —       | Repository owner (user or org).                               |
| `repo`    | string | yes      | —       | Repository name.                                              |
| `author`  | string | yes      | —       | GitHub login whose authored and engagement activity is measured. |
| `since`   | string | yes      | —       | Window start, an RFC3339 timestamp.                           |
| `until`   | string | no       | now     | Window end, an RFC3339 timestamp.                             |

It returns six **decomposed counts** under `counts` — `commitsAuthored`, `issuesOpened`, `pullRequestsOpened`, `reviewsSubmitted` (others' PRs), `pullRequestsEngaged` (commented, not authored), and `issuesEngaged` — each a `{ count, fidelity }` pair, plus the echoed `author`/`since`/`until` and the optional top-level `rateLimit`. The counts are never summed; weighting and the attention verdict stay caller-side.

There are **no list/fetch truncation seams** here (these are counts, not bounded lists) and degradation is **all-or-nothing**: any fetch failure surfaces as a tool-call error (a throttle names its retry instant), and an unresolved `author` login is a named error rather than six zeros — a silently-partial count would understate attention. Because it inherits the operator's `gh` credentials, it can measure private repositories the user-rooted contributions query cannot reach.

> **Per-category fidelity is part of the contract.** The categories are not equally precise, and each `count` carries a `fidelity` label saying so: `commitsAuthored` is the default-branch commit count attributed to the author's linked identity (it misses squash-merged and email-unlinked commits), while the five search-derived counts are search-index-approximate and — for reviews and engagement — windowed by the item's activity rather than the exact comment/review date. A caller reads each count through its label rather than as uniform ground truth.

## `authored_activity_batch`

The **batched attention** read: the same per-user measure as [`authored_activity`](#authored_activity), fanned out across several repositories in one call — the shape a cross-project attention audit reaches for directly instead of looping single-repo calls. Composite struct: `authored.BatchFacts` in `internal/authored/`.

Like `authored_activity` it is **author- and window-driven and reads no manifest conventions**, but takes a list of repositories instead of one:

| Parameter | Type     | Required | Default | Bounds | Meaning                                                  |
| --------- | -------- | -------- | ------- | ------ | -------------------------------------------------------- |
| `repos`   | string[] | yes      | —       | 1–50   | The repositories to measure, each an `owner/repo` slug.  |
| `author`  | string   | yes      | —       | —      | GitHub login whose activity is measured.                 |
| `since`   | string   | yes      | —       | —      | Window start, an RFC3339 timestamp.                      |
| `until`   | string   | no       | now     | —      | Window end, an RFC3339 timestamp.                        |

It returns one `RepoActivity` entry per repository under `repos`, in request order, each either **available** with the same six `{ count, fidelity }` counts as `authored_activity`, or **unavailable** with a reason (`not_found`, `rate_limited`, `fetch_failed`) and — for a throttle — the `resetAt` instant. The batch echoes `author`/`since`/`until` once and carries a single aggregated top-level `rateLimit`.

**Degradation is per repository, not all-or-nothing** — the one contract difference from the single-repo tool. A repository that is missing or throttled degrades only its own entry while the rest return their counts. The aggregated `rateLimit` is the tightest remaining across the successful repositories, or — when any was throttled — `{ remaining: 0, resetAt }` at the earliest throttle reset, so a caller is never told it has budget mid-throttle. Two conditions still fail the whole call: a pre-fetch validation error (an empty or oversized `repos` list, a malformed or duplicate slug, an unparseable or inverted window), and an **unresolvable** `author` login — repo-independent, so it fails every repository identically and surfaces as one named error rather than N markers.

> **A resolved-but-wrong login yields honest zeros.** The whole-batch author error catches only an *unresolvable* login; a login that resolves to a real but unrelated account returns genuine-looking zeros that no tool can distinguish from real inactivity — the same limit the single-repo tool carries.

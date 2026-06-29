# Manifests

A manifest supplies a repository's conventions — its label taxonomy, thresholds, and formats — declaratively, so a single server serves any repository without code changes. Each repository's entry is deep-merged over generic defaults: you declare only what differs.

Manifests are **operator-supplied**, not stored in the target repository. They live in your own config, which lets Overstory survey arbitrary repositories (including ones you don't control) without those repositories adopting anything.

The authoritative source for every default and validation rule below is `internal/manifest/manifest.go` (`Defaults()` and `validate()`). This page tracks it; when they disagree, the code wins.

## Discovery

Overstory finds manifests one of two ways:

1. **`OVERSTORY_MANIFESTS`** — if set, a colon-separated list of manifest file paths, resolved in order. Every listed file must exist (a missing one is an error). This overrides the drop-in directory entirely.
2. **Drop-in directory** — otherwise, every `*.yml` and `*.yaml` file in `$XDG_CONFIG_HOME/overstory/manifests.d/` (falling back to `~/.config/overstory/manifests.d/` when `XDG_CONFIG_HOME` is unset). Files are processed in sorted order.

### Public / private layering

The public/private split is a *metadata*-leak concern (private repository names), not a secrets concern — so the answer is layering, not encryption. Commit a public manifest, and keep private or work-org entries in a gitignored file (e.g. `*.private.yml`) or a file outside any repository named only via `OVERSTORY_MANIFESTS`. The discovered files compose into one set, but **each repository's entry must live whole in exactly one file** (see the single-file rule below).

## Keys

Each top-level key is a repository slug, `owner/repo`:

- Matching is **case-insensitive** (`Acme/Widgets` matches a lookup for `acme/widgets`).
- A key must be exactly two non-empty, slash-separated parts with no internal whitespace. A malformed key (`acme /widgets`, `acme/widgets/extra`) is a hard error.
- A key defined more than once **within a file** (case-insensitively) is a hard error — rather than silently keeping one.
- A key defined in **more than one discovered file** is a hard error — consolidate it into one file. This is why a single repository's entry can't be split across the public/private layer.

A repository with no matching entry resolves to the generic defaults.

## Deep-merge semantics

Defaults are the base; your entry overrides field by field. The distinction between *omitting* a field and setting it *explicitly empty* is meaningful:

- **Omitted** — inherits the default.
- **Explicit value** (including an explicit empty list `[]`) — replaces the default. An empty list is how you *opt out* of a default-on reduction (e.g. `summary.bugLabels: []` turns off bug flagging; `areaBalance.prefixes: []` disables the default area prefixes; `milestoneTracks.headingLevels: []` disables heading markers, leaving bold run-in markers).

`areaBalance` merges its two fields independently (omit `prefixes` to keep the defaults while setting `labels`). List-valued conventions — `deferred.labels`, `quality.requiredCategories`, `trajectory.windows`, `summary.bugLabels`, `milestoneTracks.headingLevels`, `milestoneTracks.labelStoplist`, `criticalPath.streams` — are whole-list replaces, not element merges.

## Minimal example

The smallest entry that overrides one convention and inherits the rest:

```yaml
acme/widgets:
  staleness:
    thresholdDays: 45
```

A fuller entry touching several blocks:

```yaml
acme/widgets:
  staleness:
    thresholdDays: 45
  deferred:
    labels: [deferred, blocked]
  areaBalance:
    labels: [http, networking]   # prefixes omitted -> default area/ prefixes still apply
  summary:
    bugLabels: [bug, defect]
```

## Schema

Every block is optional; an omitted block inherits its defaults entirely. Within a block, every field is optional and follows the omitted-vs-explicit rule above.

### `staleness`

| Field           | Type | Default | Notes                                                  |
| --------------- | ---- | ------- | ------------------------------------------------------ |
| `thresholdDays` | int  | `30`    | Inactivity days at/beyond which an issue is stale. > 0. |
| `fetchLimit`    | int  | `200`   | Cap on open issues fetched for the reduction. > 0.      |

### `deferred`

| Field    | Type       | Default      | Notes                                                                  |
| -------- | ---------- | ------------ | ---------------------------------------------------------------------- |
| `labels` | `[]string` | _(none)_     | Labels marking an open issue as parked. No default — "deferred" is repo-specific; omitted or empty means the reduction reports itself not-configured. |

### `areaBalance`

Areas are identified by explicit labels and/or prefix rules, unioned.

| Field      | Type           | Default                                          | Notes                                          |
| ---------- | -------------- | ------------------------------------------------ | ---------------------------------------------- |
| `labels`   | `[]string`     | _(none)_                                         | Labels that name an area directly.             |
| `prefixes` | `[]PrefixRule` | `area/`, `area:`, `area-` | Prefix rules; set `[]` to disable the defaults. |

A **`PrefixRule`** matches a label that starts with `prefix` + `delimiter`; the area name is the remainder:

| Field       | Type   | Notes                                                              |
| ----------- | ------ | ----------------------------------------------------------------- |
| `prefix`    | string | Required, non-empty (an empty prefix would match every label).    |
| `delimiter` | string | Required, non-empty exactly (a whitespace delimiter like `": "` is legitimate). |

### `quality`

| Field                | Type             | Default | Notes                                                                 |
| -------------------- | ---------------- | ------- | --------------------------------------------------------------------- |
| `minBodyLength`      | int              | `1`     | Min trimmed body length for a body to read as substantive. `0` disables the body check; must be >= 0. |
| `requiredCategories` | `[]CategoryRule` | _(none)_ | Label families every issue should carry one of. No default — repo-specific. |

A **`CategoryRule`**:

| Field      | Type           | Notes                                                            |
| ---------- | -------------- | --------------------------------------------------------------- |
| `name`     | string         | Required, non-empty, unique (case-insensitive) within the list. |
| `labels`   | `[]string`     | Labels satisfying the category.                                  |
| `prefixes` | `[]PrefixRule` | Prefix rules satisfying the category. A category must declare at least one of `labels`/`prefixes`. |

### `overlap`

| Field                      | Type    | Default | Notes                                                                 |
| -------------------------- | ------- | ------- | --------------------------------------------------------------------- |
| `titleSimilarityThreshold` | float64 | `0.5`   | Char-trigram Sørensen–Dice score two titles must reach to be linked as candidate duplicates. In `[0,1]`; `0` disables, `1` requires an exact normalized match. `NaN` is rejected. |

### `trajectory`

| Field        | Type    | Default      | Notes                                                          |
| ------------ | ------- | ------------ | -------------------------------------------------------------- |
| `windows`    | `[]int` | `[7, 30, 90]` | Cumulative lookback windows in days. At least one, each > 0.   |
| `fetchLimit` | int     | `500`        | Cap on recently-updated issues (open and closed) fetched. > 0. |

### `summary`

Conventions the `project_summary` reduction consumes.

| Field                 | Type       | Default   | Notes                                                            |
| --------------------- | ---------- | --------- | --------------------------------------------------------------- |
| `prStalenessDays`     | int        | `14`      | Inactivity days past which an open PR reads as stale. > 0.       |
| `unmilestonedAgeDays` | int        | `30`      | Age past which an unmilestoned open issue is a hygiene signal. > 0. |
| `prFetchLimit`        | int        | `200`     | Cap on PRs fetched. > 0.                                         |
| `milestoneFetchLimit` | int        | `100`     | Cap on milestones fetched. > 0.                                  |
| `bugLabels`           | `[]string` | `["bug"]` | Labels marking an issue as a bug for recommendation inputs. Set `[]` to opt out. |

### `milestoneTracks`

Conventions the `milestone_tracks` reduction consumes: how the track structure operators encode in a milestone's *description* is recognized. A track is a labeled section carrying issue references; markers degrade to zero tracks on prose/empty descriptions, so the defaults are safe on any repository.

| Field           | Type       | Default                                                        | Notes                                                                                              |
| --------------- | ---------- | ------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| `headingLevels` | `[]int`    | `[2, 3]`                                                       | Markdown heading depths that start a track. Each in `[1,6]`. An explicit `[]` disables heading markers (valid — `boldRunIn` is independent). |
| `boldRunIn`     | bool       | `true`                                                        | Treat a bold run-in label (`**Label** (status):`) as a track start. Set `false` to disable; distinct from omission. |
| `fetchLimit`    | int        | `100`                                                         | Cap on open milestones fetched. > 0.                                                               |
| `labelStoplist` | `[]string` | common prose-section labels (`Why`, `Ikigai`, `History`, …) | Marker labels that are prose sections, not tracks (matched case-insensitively). Extend it for your repo's own section headings. |

Setting both `headingLevels: []` and `boldRunIn: false` disables all markers — a valid no-op that yields zero tracks for every milestone, not a configuration error.

### `criticalPath`

Conventions the critical-path / gate block (surfaced by `project_summary` and `backlog_review`) consumes: the repo's ordered list of work streams that form its critical path, and the label marking an issue as on that path. Streams *are* areas — classification reuses the `areaBalance` taxonomy — so a stream named `simulation` matches an issue labeled `area/simulation`. There is no generic default; a critical path is repo-specific (like `deferred`), so a repo that declares neither field leaves the block not configured.

| Field     | Type       | Default | Notes                                                                                                          |
| --------- | ---------- | ------- | -------------------------------------------------------------------------------------------------------------- |
| `streams` | `[]string` | none    | Ordered stream names, referencing canonical area names (the part after the `area/` prefix). Whole-list replace. |
| `label`   | string     | none    | The label marking an issue as on the critical path (matched case-insensitively).                               |

Both fields are declared together or not at all: setting one without the other — including an explicit empty `streams: []` alongside a `label` — is a configuration error, not a silent no-op. Stream names are trimmed, must be non-empty, and must be unique (case-insensitively).

### `response`

The byte budget the composite reductions (`project_summary`, `backlog_review`) trim their detail lists to fit, so a large repository's response degrades gracefully instead of exceeding the MCP client's tool-result token cap and failing. This is an operational knob, not a repo-taxonomy convention — like the per-block `fetchLimit`s, it has a generic default and a per-repo override.

| Field      | Type | Default | Notes                                                              |
| ---------- | ---- | ------- | ----------------------------------------------------------------- |
| `maxBytes` | int  | `20000` | Byte budget per serialization. Must be `>= 4096`. See note below. |

The default is deliberately conservative: the MCP SDK serializes the facts twice on the wire (once as structured content, once as a back-compat text block), so the wire payload is roughly twice `maxBytes`, and the default leaves headroom below a typical ~25k-token cap across plausible bytes-per-token ratios. A normal-sized response is never trimmed and is byte-identical to an unbounded server; only an over-budget response is bounded, and it then carries a top-level `sizeBound` marker (see [Tools](./tools.md)).

## Validation

A manifest that is unreadable, unparseable, or declares an invalid value is a hard error that names the offending file and repository. Unknown fields are tolerated — a manifest may carry config for reductions a given build doesn't yet implement.

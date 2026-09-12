# Forge backends

> **Status: design, not built.** Nothing described here exists in the code. This document records the intended direction for serving repositories hosted on a forge other than GitHub, starting with Forgejo, and the questions that direction leaves open. It is not guidance to act on. As each part lands, its settled decisions move into the key design decisions in `CLAUDE.md` and that part is removed from this document. When nothing remains, the document is deleted.

Code references describe the tree at commit `16f1f6b31bdd` and name identifiers rather than line numbers.

Forgejo API claims are checked against the published API specifications of the stable release lines `v15.0.8` and `v16.0.4`. The two agree on everything cited here except where a difference is named.

Observations from a live instance name that instance and the version it reported.

## Why

A repository is addressed as `owner/repo` and resolved against exactly one host, GitHub, and the host is not part of the address. That leaves two gaps:

- **A repository hosted elsewhere cannot be surveyed at all.** This gap announces itself: the call fails.
- **A repository that moves off GitHub keeps being answered from the copy it left behind.** This gap does not announce itself. If the move leaves an archived or abandoned copy, every block of the response is well formed, and every value is frozen at the moment that copy stopped being the live repository. Nothing in the response distinguishes this from a reading of the live project.

The problem is recorded in #133.

## Invariants a second backend preserves

- **The tool surface and the shape of the facts do not change.** A caller does not branch on which forge produced a response, except where a fact states that an input was unavailable or does not apply.
- **The server reduces; the caller renders.**
- **An input that was not read is never reported as read and found clear.** `SeamState` and `Capabilities` already carry this for the readiness inputs. Every other input a backend might fail to supply needs the same treatment (see [Inputs without an availability signal](#inputs-without-an-availability-signal)).
- **Targeting stays explicit.** There is no ambient default repository and no ambient default forge.
- **The server is read-only against every forge.** It issues reads; it creates, edits, closes and labels nothing.
- **A credential never appears in a log or a returned error.**
- **A credential is sent only to the host that issued it, including across redirects.** A second backend gets its own authentication path rather than a redirected GitHub one. Today's token source runs `gh auth token` without naming a host, so binding each credential to its host is new work for both backends.

## Where GitHub is built in today

- **One data interface, one implementation.** Every tool fetches through `github.Fetcher`. `GraphQLFetcher` is its only implementation, and it also performs the REST issue-events fetch.
- **One fetcher and one credential for every repository.** `server.New` constructs a single fetcher and hands it to every tool handler. That fetcher holds one `GHTokenSource`, which runs `gh auth token` once and caches the result for the life of the process.
- **Hardcoded endpoints.** `defaultEndpoint` and `defaultRESTEndpoint` name GitHub's API, and requests carry GitHub-specific headers.
- **No host in identity.**
  - Single-repository tools take `owner` and `repo`.
  - Batch tools take `owner/repo` strings.
  - `Fetcher` methods take the joined `owner/repo`.
  - Every `repo` field in tool output is that same string.
- **Domain types live in the GitHub package.** `Issue`, the fetch result types, `SeamState` and `Capabilities` are declared in `internal/github`, and the reductions import them from there. The importing set is derived with

  ```sh
  grep -rl '"github.com/jakewan/overstory/internal/github"' internal cmd --include='*.go' | grep -v _test.go
  ```

  and spans `authored`, `backlog`, `criticalpath`, `dependency`, `maintenance`, `reduce`, `server` and `summary`.
- **Failures are worded for GitHub.** The sentinel errors name `gh` and GitHub (`ErrGHNotFound`, `ErrGHNotAuthed`, `ErrRateLimited`).
  - Single-repository tools return a fetch failure as a tool error wrapping that text.
  - Batch tools map failures onto per-repository unavailability markers such as `not_found`, `rate_limited` and `fetch_failed`.
- **Manifest resolution is per tool.** `backlog_review`, `project_summary` and `milestone_tracks` resolve a manifest entry. The activity tools and their batch forms never read the manifest.

## Repository identity

### What refusal means

The first change needs no second backend: record which forge a repository lives on, and refuse to answer for a repository **marked as living on a forge this server cannot serve**.

The test is the marker, not reachability. The frozen GitHub copy of a moved repository *is* reachable, and answering from it is the failure being closed.

### Entry paths

A marker protects only the paths that read it:

| Entry path | Reads the manifest today | Covered by a manifest marker |
| --- | --- | --- |
| Manifest-driven tool, repository has an entry | Yes | Yes |
| Manifest-driven tool, repository has no entry | Resolves to defaults. `Resolve` reports that nothing matched, but only `backlog_review` keeps that result | No |
| Single-repository activity tool | No | Only if the activity tools start resolving the manifest |
| Batch activity tool | No | Only if each fanned-out repository is resolved |

### Where the marker lives (open)

Two shapes, each with its own forces:

- **A field on the repository's manifest entry.** It fits the existing schema, where each entry is a set of optional blocks. But manifest decoding tolerates unknown fields, so a manifest can carry configuration a binary does not implement yet. A binary that predates the field therefore ignores it and answers from GitHub, which is the stale answer again. How much that matters depends on how installs track changes. Overstory cuts no tagged releases, and its supported surface is the current `main` branch.
- **A host-qualified key.** It makes the same `owner/repo` on two hosts expressible, which today's key rule rejects as a duplicate. But `loadFile` rejects any key that is not exactly `owner/repo`, so an older binary fails the whole manifest load. That refuses rather than answering wrong, but it does so for every repository, GitHub ones included.

Forces that apply to both shapes:

- **A manifest marker cannot reach repositories without an entry, or the activity tools.** See the table above.
- **A forge host is sensitive metadata.** Manifest keys are already treated as sensitive, and the public/private manifest layering exists to keep private names out of committed configuration. A host in a key or a field inherits that concern.
- **An observed signal needs no configuration.** GitHub reports whether a repository is archived (`Repository.isArchived`). Refusing or flagging an archived repository covers every entry path with no manifest entry at all. It does not catch a copy left unarchived, so it complements a marker rather than replacing it. Which signal is primary is open.

### How comparable tools address repositories across hosts

A September 2026 survey of tools that work with repositories on more than one host, read from each tool's documentation and source, splits them into two groups.

- **One host per run, process or client; repositories are bare slugs.** Renovate sets `platform` and `endpoint` once in global configuration and cannot override them per repository. GoReleaser, the Gitea MCP server and Drone's `go-scm` library each fix the host for a whole run, process or client. A bare slug is unambiguous there only because the host cannot vary.
- **Many hosts at once; the host travels with the reference.**
  - The GitHub CLI accepts `[HOST/]OWNER/REPO`, where a bare `OWNER/REPO` resolves to a default host.
  - The GitLab CLI accepts a host-qualified form and full URLs, and also falls back to a default host.
  - Backstage carries a full URL on every reference and matches its host against a list of per-host integrations.
  - `tea` and git-bug bind a repository to a named login or bridge that carries the host.

No surveyed tool puts a host field on a per-repository entry beside a bare slug. Overstory serves many repositories from one process, which places it in the second group unless it runs one server per host.

Credentials are keyed by host almost everywhere: per-host entries in the GitHub and GitLab CLIs' configuration, Backstage's per-host integrations, Renovate's host rules. Environment-variable tokens are where that binding gets bypassed. The GitHub CLI guards against it: `GH_TOKEN` applies only to `github.com` and `ghe.com` hosts, and GitHub Enterprise Server hosts take a separate variable. `tea` does not: its environment login always overrides the login matched from the repository's remote.

For the two shapes above, this means the prevailing form is a host-qualified reference that falls back to a default host. That form keeps every existing `owner/repo` key meaning GitHub, but it is also the form an older binary rejects outright. Which shape overstory adopts stays open.

## Backend contract

- **Where the domain types live (open).** A second implementation has to produce what the reductions consume. The alternatives:
  - move the types into a forge-neutral package first
  - rename `internal/github` in place
  - have a Forgejo package produce the existing `github` types

  The first two touch every importer listed above. The third leaves a misleading package name at the centre of the design.
- **Choosing a backend per repository.** Two approaches:
  - **A routing `Fetcher` built by `server.New`** that selects an implementation per `owner/repo`. No handler signature changes, but the router needs its own manifest resolver, since the activity handlers are not given one.
  - **Resolution inside each handler.** The batch fan-outs complicate this, because they pass only a repository string.
- **Forge-neutral errors.** Sentinel errors and their messages stop naming `gh` and GitHub where the failure is not GitHub's.
- **Capabilities, per repository.** `Capabilities` and `ApplyCapabilities` already express a relationship a repository does not carry. Today only `IssueListResult` carries capabilities, and the GitHub fetch never sets them.
  - On Forgejo, `internal_tracker.enable_issue_dependencies` on the repository object says whether dependency edges exist for that repository.
  - Neither stable line has a sub-issue hierarchy, so `NoSubIssueHierarchy` holds for every Forgejo repository.

## Where each read comes from on Forgejo

| `Fetcher` method | Forgejo source | Shape |
| --- | --- | --- |
| `ListOpenIssues` | `GET /repos/{owner}/{repo}/issues` with `state=open` and `type=issues`. Per issue: `…/issues/{index}/dependencies`, `…/blocks`, `…/comments`, `…/timeline` | A direct list. Edges, last activity and cross-references are split into per-issue calls |
| `ListOpenIssuesWithLabel` | The same list, filtered by `labels` | Direct |
| `ListIssuesUpdatedSince` | `…/issues` with `state=all`, `type=issues`, `since` | Direct |
| `ListPullRequestsUpdatedSince` | `…/issues` with `state=all`, `type=pulls`, `since`. The pull-request list itself has no `since` | Direct |
| `ListOpenMilestones` | `GET /repos/{owner}/{repo}/milestones` with `state=open` | Direct |
| `ListOpenPullRequests` | `GET /repos/{owner}/{repo}/pulls` with `state=open`. Per pull request: `…/commits/{ref}/status` | A direct list. CI state is split into per-PR calls |
| `AuthoredActivity` | `…/issues` with `created_by`; `…/issues/comments`; per pull request `…/pulls/{index}/reviews`; `…/commits`; `/users/{username}` | Split into per-item calls. The commit count is rebuilt, with divergence |
| `ListIssueEvents` | `…/issues` with `state=all` and `since`, both types. Per item: `…/issues/{index}/timeline` with `since` | Split into per-item calls |

Every method has a source. The gaps sit at the level of individual fields, below.

### Open issues

- **Body text.** `body` is raw markdown, and the render endpoints (`/markdown`, `/markdown/raw`) return HTML. So `BodyText`, which is plain text with markdown and template scaffolding stripped, needs a local strategy. The quality reduction's length check measures that text, so a different stripping rule changes what the check means. The strategy is open.
- **Open total.** The specification does not declare `X-Total-Count` on the issue list. It does declare it on the comment, milestone, review and timeline lists, and Codeberg sends it on the issue list regardless. The repository object's `open_issues_count` is a candidate source for the open total. Which source is authoritative is to be settled on a dev instance.
- **Dependency edges.**
  - The dependency and blocks responses return `Issue` objects carrying `repository.full_name`, so cross-repository edges can be dropped as they are today.
  - Those responses are declared without paging headers, so how a capped edge list is detected is open.
  - `internal_tracker.enable_issue_dependencies` decides `NoBlockedByEdges` per repository.
  - There is no sub-issue hierarchy, so `SubIssuesTotal` and `SubIssuesCompleted` carry no weight.
- **Last activity.** `LastActivityAt` excludes bot comments today by account type and by a login suffix. Forgejo's `User` has no bot flag, so that exclusion has no direct source.
- **Cross-references.** Timeline rows carrying `ref_issue` (an `Issue`) are the candidate source for `ReferencedBy`. Which row types and which direction correspond to GitHub's cross-reference event is to be settled.

### Activity windows

The issue list's `since` and `before` filter on update time, matching the updated-since window the GitHub fetch reads.

GitHub clears an issue's `closedAt` when it is reopened, which gives the trajectory reduction its "open as of now" reading. Whether Forgejo's `closed_at` behaves the same way is unconfirmed.

### Milestones

Milestones carry an `id` and no per-repository number or URL. So `MilestoneRef.Number` and `Milestone.URL` need a mapping: the `id`, and a constructed link.

The milestone list declares `X-Total-Count` for the open total.

### Open pull requests

- **Direct fields.** `draft`, `head.ref` and `updated_at` map directly.
- **CI state.** It comes from the combined commit status. Its vocabulary is `pending`, `success`, `error`, `failure` and `warning` on `v15.0.8`, with `skipped` added on `v16.0.4`. That is not GitHub's check-rollup vocabulary, so `CIStatus` needs an explicit mapping, and the set it maps from varies by version.
- **Open total.** The pull-request list does not declare `X-Total-Count`.

### Authored activity

- **The search endpoint's person filters are the wrong subject.** `/repos/issues/search` offers `created`, `reviewed`, `assigned` and `mentioned`, and each filters by *the authenticated user*. Mapping author filters onto them counts the token holder for every author requested, and passes every test in which the author and the token holder are the same person. Author-specific reads use the repository issue list's `created_by` instead (`assigned_by` and `mentioned_by` also exist). A fixture where the author is not the token holder guards the difference.
- **Opened counts.** `created_by` over `…/issues`, with `since` as the lower bound. `since` filters on update time, which gives a superset, so creation time is filtered locally.
- **Engaged counts.** The repository comment list also filters on update time (`since`, `before`). Comments are filtered locally by author and creation time, and attributed to issues or pull requests by `issue_url` or `pull_request_url`, excluding items the author opened.
- **Reviews.** One `…/pulls/{index}/reviews` read per pull request active in the window, filtered by reviewer and `submitted_at`.
- **Commits.** `…/commits` has no author or date filter: `sha`, `path`, `not`, paging, and flags for stats, verification and file lists, each on by default. The count is rebuilt by walking from the default branch's head with those flags off and filtering locally. That diverges from GitHub's exact count in two ways:
  - **No stopping point.** Commit history is not ordered by date, so any stop rule either reads further back than the window or risks stopping early.
  - **Attribution.** A commit is attributed to an account only through the email on the commit. Commits whose address is not registered on the instance resolve to no one, and that includes GitHub no-reply addresses in migrated history.

  Where attribution cannot be confirmed, the count carries an availability marker rather than a bare number. `AuthoredActivityResult` has no such marker today.

### Maintenance activity

- **No events stream.** GitHub's repository-wide issue-events stream has no Forgejo counterpart. The repository activity feed is not a substitute: its operation types include closing and reopening but not labelling, milestoning, assignment or renaming, and it pages by a single date without a cursor.
- **The read decomposes instead.** List the issues and pull requests updated since the window start, then read each one's timeline from the same instant. Timeline rows carry the actor (`user`) and per-type payloads:
  - `label`, with a `body` of `"1"` on an add and empty on a removal
  - `milestone` and `old_milestone`
  - `assignee` with `removed_assignee`
  - `old_title` and `new_title`

  The label and assignee payloads were observed on Codeberg (`16.0.0-dev-741`).
- **Premise: every mutation the reduction counts bumps the item's `updated_at`.** A mutation that doesn't is invisible to the listing, so its events are silently dropped.
  - **Observed on Codeberg (`16.0.0-dev-741`):** a label change, a title change, a comment, and cross-, commit- and pull-request references each left a timeline row timed exactly at the item's `updated_at`.
  - **Unconfirmed:** milestone changes, assignment and assignee removal. These are to be confirmed on a dev instance.
  - **The listing also over-selects.** An item's `updated_at` can move with no new timeline row (observed). That costs requests, not correctness.
- **`ViaAutomation` has no source.** Timeline rows carry no performed-via field, and `User` has no bot flag.
- **Truncation has two levels:** the item listing, and each item's timeline. Neither gives the coverage proof the events stream gives, which is scanning back past the window floor.
- **Deduplication.** `EventID` deduplicates across pages today using GitHub's monotonically increasing event id. Timeline rows carry an `id`, but its ordering is not established.

### Inputs without an availability signal

The readiness inputs have `SeamState`. These inputs have no availability signal:

- `Blocking` and `ReferencedBy`, which carry truncation flags only
- `LastActivityAt` and `BodyText`, which carry neither
- every `AuthoredActivityResult` count
- `IssueEvent.ViaAutomation`, where `false` reads as "not automation"

A backend that cannot supply one of these yields a silent zero or `false`, the same failure the readiness seams exist to prevent. Each needs an availability signal before a backend that can withhold it ships.

## Authentication

- **Token source (open).** Forgejo has no `gh`, so the second backend needs a token source of its own: an environment variable, a credentials file, or a forge CLI's stored login.
- **HTTPS-only (open).** A dev instance may not terminate TLS.
- **Host binding (settled).** Whatever the source, a credential is bound to the host that issued it (see [Invariants](#invariants-a-second-backend-preserves)). The survey above shows environment-variable tokens as the usual way that binding gets bypassed, so an environment-variable source names the host it applies to.

## Budget and truncation

- **What `RateLimit` means today depends on the pool.** It is GraphQL points for most fetches and the REST core pool for the issue-events fetch.
- **Forgejo declares no rate-limit headers.** Its specification declares only paging headers: `Link`, `X-HasMore`, `X-Page`, `X-PageCount`, `X-PerPage` and `X-Total-Count`. An instance can add its own: Codeberg sends `ratelimit-policy` and `ratelimit` (observed on `16.0.0-dev-741`). Those belong to that instance, not to Forgejo. An instance that sends none maps onto the existing unknown-budget path, where `RateLimit` is omitted.
- **Per-item calls multiply requests.** A window's cost scales with the number of items active in it, including items whose update left no timeline row.
- **Truncation is page-based.** Totals exist only where a list declares `X-Total-Count` or the repository object carries a counter. Edge lists are declared without paging headers, and the maintenance read truncates at two levels.

## Validation

- **Fixture tests** follow the existing REST issue-events tests (`TestListIssueEvents*` in `internal/github/graphql_test.go`): an `httptest` server serving recorded payloads. The struct-against-query contract check is specific to GraphQL and has no counterpart here.
- **A fixture where the requested author is not the token holder** is required for authored activity.
- **An opt-in live test** mirrors `TestLiveSchemaAcceptance`, gated on its own environment variables. The existing live test does not exercise `AuthoredActivity`, and the Forgejo one should.
- **A public instance** (anonymous reads, subject to that instance's anonymous budget and running whatever build it runs) can confirm read shapes.
- **A dev instance** is needed for:
  - authentication
  - the `updated_at` premise for milestone and assignment changes
  - commit-email attribution, including migrated no-reply addresses
  - `closed_at` after a reopen
  - the per-repository dependency switch
  - whether migrating a repository from GitHub preserves issue and pull-request numbers
- **Tool-level tests need no backend-specific fake.** `fakeFetcher` returns canned results and ignores the repository except in its per-repository batch maps.

## Ordering and change surfaces

Two ordering constraints hold for any operator:

- The identity marker comes before serving any repository that has moved.
- A backend comes before any repository the server is expected to survey moves onto that forge.

Each change touches its own surfaces:

- **When the identity marker ships:**
  - `docs/src/manifest.md`, since the manifest format changes
  - `docs/src/tools.md`, where refusal becomes observable
  - a `CHANGELOG.md` entry
- **When the first repository moves:** the render-skill guides under `docs/src/guide/render-skills/` resolve `owner/repo` with `gh repo view` and have no fallback.
- **When the backend ships:**
  - `README.md`, and the book's introduction, installation, tools and render-skill pages
  - the credential model in `SECURITY.md`
  - the scope statement in `CONTRIBUTING.md`
  - `CLAUDE.md`
  - `.github/copilot-instructions.md`
  - the tool `Description` strings
  - runtime strings in the forge-neutral layer, such as the "not a GitHub user" error
  - doc comments outside `internal/github` that describe GitHub's behaviour as if it were the data's own: `closedAt` cleared on reopen in the trajectory reductions, rendered plaintext bodies in the deferred and recommendation reductions, `subIssuesSummary`, the event vocabulary and monotonic event ids in the maintenance reduction, and the `cmd/overstory` package comment

  Error strings and comments inside the GitHub implementation stay correct and are not surfaces.

The lists are derived, not maintained by hand:

```sh
grep -rli github README.md SECURITY.md CONTRIBUTING.md CLAUDE.md CODE_OF_CONDUCT.md .github/copilot-instructions.md docs/src
grep -rn --include='*.go' 'GitHub' internal cmd | grep -v '_test.go:' | grep -v '^internal/github/'
```

Hits that concern only where this repository itself is hosted, such as its security-report channel and supply-chain automation, belong to this repository's hosting, not to the product. They are out of scope here.

## Open questions

- **Identity:** a field or a host-qualified key, and whether the marker or the archived signal is primary.
- **Credentials:** the token source for a second backend, and whether HTTPS is required.
- **Versions:** which Forgejo release lines are supported, and for how long.
- **Body text:** how `BodyText` is produced from raw markdown.
- **Package layout:** where the domain types live.
- **Commit attribution:** how commit emails map to accounts, including migrated history.
- **Migration numbering:** whether migrating a repository preserves issue and pull-request numbers. Issue references such as #133 in this repository's history depend on it.
- **Automation:** whether `ViaAutomation` can be approximated at all.
- **Rate limiting:** whether a self-hosted instance's API sits behind a rate-limiting proxy that reports a budget.

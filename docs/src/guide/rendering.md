# Rendering the Facts

Overstory's tools return **structured facts, never prose**. A tool call answers "what does the backlog look like" with a JSON composite — counts, lists, flags — and renders nothing of its own. Turning those facts into a report an operator reads is the *caller's* job: the agent that called the tool decides how to narrate, what to lead with, and which judgments to layer on top. This page shows what that consumption looks like end to end, so the caller-side half of the design is concrete rather than inferred.

This split is deliberate. The server reduces a large, noisy issue landscape to compact facts; the caller renders them into whatever report its audience wants. It is what lets one server serve many callers — a Claude skill, a Cursor skill, a one-off prompt — each rendering the same facts in its own voice. The facts are the contract between the two halves; everything below renders *from* them.

## A worked example: `project_summary` facts → report

Below is a **trimmed snapshot** of what `project_summary` returns for a small repository — Overstory's own, captured during a real call. It is illustrative, not exhaustive: the real payload also carries `areaInventory`, `criticalPath`, and a conditional `rateLimit` block, and each block has more fields than shown. The Go structs in `internal/summary/` are the authority on the shape — their `json:"..."` tags *are* the wire contract — and [Tools & Facts](../tools.md) documents what each block means. This snapshot is here to show the *pattern*, not to enumerate the schema.

```json
{
  "repo": "jakewan/overstory",
  "generatedAt": "2026-06-24T12:26:41Z",
  "milestones": { "available": true, "openMilestones": 0, "milestones": [] },
  "hygiene": {
    "missingArea": {
      "count": 2,
      "issues": [
        { "number": 41, "title": "Repository is not audited for safe public/open-source release", "ageDays": 7, "inactiveDays": 7 },
        { "number": 53, "title": "Per-repo reductions cover authored activity but not maintainer state-mutation activity", "ageDays": 0, "inactiveDays": 0 }
      ],
      "listTruncated": false
    },
    "stale": { "count": 0, "issues": null, "listTruncated": false }
  },
  "openPRs": { "available": true, "openPRCount": 0, "pullRequests": [] },
  "recommendations": {
    "candidates": [
      { "number": 43, "title": "No grounded basis for prioritizing which further reductions / PM signals overstory should surface", "isBug": false, "ageDays": 7, "inactiveDays": 0 },
      { "number": 41, "title": "Repository is not audited for safe public/open-source release", "isBug": false, "ageDays": 7, "inactiveDays": 7 }
    ],
    "listTruncated": false
  }
}
```

A render skill walks the blocks in order and turns each into a section. The same facts above become:

```markdown
## Active Milestones

No open milestones.

## Hygiene Signals

- **Missing area label** (2): #41 — Repository is not audited for safe public/open-source release (age 7d); #53 — Per-repo reductions cover authored activity but not maintainer state-mutation activity (age 0d)
- **Stale**: None

## Open PRs

No open PRs.

## What's Next

1. **#43** — ground the next reductions before building them; aged backlog with no milestone, the natural next planning unit.
2. **#41** — the open-source release audit; aged but lower-friction than picking a reductions direction.
```

Two things to notice. The factual sections (`Active Milestones`, `Hygiene Signals`, `Open PRs`) are a near-mechanical projection of the blocks — the skill renders `count` and `issues` straight through, and renders `None` / `No open PRs` when a block is empty so the reader can see the check actually ran. `What's Next`, by contrast, is *caller judgment*: the `recommendations` block supplies neutral per-issue inputs (`isBug`, `ageDays`, `inactiveDays`, and the milestone when present) and a neutral pre-sort, but the ranking into "do this first" is the skill's, not the server's. The server reduces; the caller ranks and renders.

## Truncation is a floor, not a ceiling

The example above fits comfortably under any limit — the repository has four open issues. A real backlog does not, and that is where the caller's most important discipline lives: **a truncated result is a lower bound, never a complete picture.**

Every block surfaces this explicitly so the caller can tell incomplete data from complete:

- `fetchTruncated` — the scan window didn't cover every open issue, so counts themselves are a floor.
- `listTruncated` — more matches exist than were listed under the call's `limit`.
- `membershipTruncated` — on a milestone, its listed members are a floor relative to its open count.

When any of these is set, the render must say so — "showing 25 of 60+; this is a lower bound, not the full set" — rather than presenting the capped list as exhaustive. The failure mode this guards against is a caller reading a truncated `missingArea` list of 25 issues and reporting "25 issues need an area label" when the real count is higher and the window simply stopped. The flags are part of the contract precisely so the caller never has to guess; rendering them is not optional polish.

## Adopting a render skill

The reference skills in this section — [Project Summary](./render-skills/project-summary.md) and [Backlog Review](./render-skills/backlog-review.md) — are working examples you can copy and adapt. They are **snapshots**, not a maintained distribution: the canonical render skills live in the operator's own agent configuration, and the in-book copies will drift from the server's evolving facts. Treat them as a starting point to adapt, not a contract to depend on.

To adopt one, copy its `SKILL.md` body into your agent's skills directory under a directory named for the skill:

- **Claude Code:** `~/.claude/skills/<name>/SKILL.md`
- **Cursor:** `~/.cursor/skills/<name>/SKILL.md`

A skill's `SKILL.md` — its name, description, and body — is identical for both callers; only separate *rule* files (`.md` for Claude, `.mdc` for Cursor) would differ, and these render skills carry none. Adapt the copy to your repository's conventions as the server's facts evolve, rather than treating the snapshot as a fixed interface.

# Design Decisions

How value-laden design forks are settled in this project.

## Adjudicating Design Forks

(extension point: `design-fork-adjudication`)

overstory is a generic, multi-tenant tool: it serves arbitrary repositories the operator does not control. So when a design fork is value-laden — where to surface a new signal, which manifest shape to adopt, which default set to ship, how to shape an output contract — settle it by **what most open-source users of a tool like this would want**, not by what fits the operator's own repository's taste. A fork resolved from one operator's conventions fails the arbitrary-repo thesis the whole tool rests on.

This lens *is* the tie-break. It is the deliberate alternative to settling such a fork by reaching for the simplest, least-code, or most-general option, or by a neutral A/B canvass that hands the judgment back to the user: ask what the broad population of users wants and let that decide.

**How to apply:**

- Ground the call in the actual public landscape — survey how comparable real-world projects handle it — rather than extrapolating from this repo's own conventions. A proportionate survey is enough; size the research to the decision.
- Present the grounded finding before recommending, so the reasoning is visible and redirectable.
- When new landscape data reshapes a decision, re-examine already-shipped sibling decisions for whether the finding transfers — but verify it actually does, since different semantics can keep the original choice correct.

This lens applies to product- and convention-level forks. Purely mechanical or internal-engineering choices (which helper to reuse, naming, internal structure) stay ordinary engineering judgment.

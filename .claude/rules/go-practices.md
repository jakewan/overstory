---
paths: "**/*.go"
---

# Go Practices

Conventions for Go code in this repository.

## Errors

- Wrap errors with context as they propagate: `fmt.Errorf("fetching issues: %w", err)`. Use `%w` so callers can `errors.Is`/`errors.As` the cause.
- Before relying on `errors.Is` to match a dependency's sentinel, confirm the cause is in the chain — `errors.Is` only traverses causes wrapped with `%w`, so one formatted with `%v` silently fails to match. When unsure, match a stable error code or type instead.
- When a typed error must both match a sentinel via `errors.Is` *and* carry recoverable detail via `errors.As`, give it a value-receiver `Is(target error) bool` and propagate it **by value**. `errors.As(err, &target)` matches on the concrete dynamic type in the chain, not on the `Is` method — so a pointer representation (`&T{}`) silently fails `errors.As(err, &valueTarget)` even though `errors.Is` still matches. Return `T{...}`, never `&T{...}`, so both resolve and both survive `%w` wrapping.
- Return errors; don't `log.Fatal` outside `main`. The single acceptable fatal is the top-level server-run error in `main`.
- Make validation errors specific and actionable — name what was wrong (which field, which repo, which manifest key) so the message stands on its own.
- Trim whitespace before checking a required string is non-empty (`strings.TrimSpace(s) == ""`), so a blank-looking value is rejected like a missing one — but only when the value's whitespace is noise (names, identifiers, labels). When whitespace is *semantically meaningful* (a delimiter, separator, or format token — e.g. a `": "` colon-space delimiter), check exact emptiness (`s == ""`) instead, so a legitimate whitespace value isn't wrongly rejected.
- When validating a float falls within a range, reject `NaN` explicitly (`math.IsNaN`) **before** the bounds check — every comparison against NaN is false, so a NaN passes `x < lo || x > hi` and is silently accepted, then poisons downstream comparisons (e.g. a similarity threshold no score can ever clear).
- `errcheck` runs with `check-blank: true`, so discarding an error to `_` is itself a lint failure — `_ = f()` does not silence an unwanted error. Capture and inspect it, or fold a secondary cleanup error into the primary one with `errors.Join(...)`. The point is to act on every error, not to suppress it.

## Context

- Functions that do I/O or are cancellable take `context.Context` as the **first** parameter.
- Don't store a `context.Context` in a struct; pass it through the call chain.
- Shell out to `gh` via `exec.CommandContext` so a cancelled context tears the subprocess down.

## MCP server

This server speaks JSON-RPC over stdio.

- **stdout is the protocol stream — write nothing else to it.** No `fmt.Println`/`fmt.Printf` to stdout in server code; diagnostics go to stderr via `log`.
- **The server reduces; the caller renders.** Tools return compact structured facts. Keep prose, markdown, and narrative judgment out of tool output — that is the caller's job.
- **Conventions are declarative.** A repository's labels, thresholds, and formats come from its manifest deep-merged over generic defaults, never from Go constants. When you reach for a `const` that encodes a project's convention, it belongs in the manifest schema instead.
- Publish a tool's input constraints — defaults, bounds (`minimum`/`maximum`), required vs optional — in its JSON schema, not in handler code. The schema is the contract callers introspect, and the SDK enforces it before the handler runs, so invalid input fails with a clear validation error instead of being silently tolerated. The installed `jsonschema-go` infers only a description from struct tags — not `default`/`minimum`/`maximum`, and it marks every non-`omitempty` field required — so a tool needing real constraints sets an explicit `*jsonschema.Schema` as `Tool.InputSchema` rather than relying on struct-tag inference.
- A literal-null or absent arguments payload is safe on the generic `mcp.AddTool` path: the SDK unmarshals it into a freshly-allocated (non-nil) map before applying schema defaults, so defaults apply cleanly with no panic. No null-guard middleware is needed; cover the defaults-apply path with a test that omits a defaulted field.
- Result-set limits must be surfaced in the structured output, never silently truncated — a caller cannot tell incomplete data from complete data otherwise.
- Smoke-testing the running binary over stdio needs a driver that holds stdin open until each reply is read. Piping a batch of requests and letting stdin close races the EOF shutdown — the session tears down before responses flush, so the binary exits 0 with no output, a false pass. Prefer the in-memory transport for automated coverage (see Tests); for a manual end-to-end check, drive the binary from a harness that reads each response before sending the next and closes stdin last.

## Tests

- Use the standard `testing` package — no external assertion or mocking frameworks.
- Prefer table-driven tests for behavior variations (valid input, invalid input, edge cases, error paths) — not just the happy path.
- Isolate filesystem state with `t.TempDir()`; register cleanup with `t.Cleanup`.
- Tests describe **what** the code does from the caller's perspective, not **how**. An interface should exist because a test needs to substitute an implementation, not as speculative abstraction.
- Exercise tool behavior through an in-memory client/server session (`mcp.NewInMemoryTransports`), asserting on the structured result — and on `IsError` for the error paths.
- The github layer's wire-decode structs are drift-checked against their GraphQL query by `TestQueryDecodeContract`, which only inspects the structs its cases enumerate. When you add a query or a decode struct, register it there — an unregistered decode type is silently unguarded, the exact drift this test exists to catch.

## Documentation

- Exported types, functions, and packages carry godoc comments beginning with the symbol's name (`// New builds …`). The `revive` `exported` lint rule enforces this — a missing or malformed comment fails CI.
- Comments explain **why** — rationale, constraints — not **what** the code already says.

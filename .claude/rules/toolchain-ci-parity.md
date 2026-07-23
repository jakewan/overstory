---
paths: mise.toml, mise.lock, go.mod, lefthook.yml, .golangci.yml, .github/workflows/ci.yml, .github/workflows/vuln.yml
---

# Toolchain / CI Parity

Local development and CI must run the same tool versions and the same formatters. When they drift, a change passes locally and fails in CI (or the reverse), or CI silently tests a different Go than developers run. These invariants are coupled across the files this rule conditions on — editing one file without its partner reintroduces the drift.

## golangci-lint version is one atomic value

The `golangci-lint` version is pinned in two places: `mise.toml` (the local binary) and the `golangci-lint-action` `version:` in `.github/workflows/ci.yml` (the CI binary). They must be identical. A config written for one minor can warn or error on another, so a mismatch means lint passes locally and fails in CI, or vice versa. When bumping, change both in the same commit.

## go.mod `go` directive tracks the mise Go pin

CI installs Go via `actions/setup-go` with `go-version-file: go.mod`, so the `go` directive in `go.mod` — not `mise.toml` — chooses the CI toolchain. Pin it to the same patch version as the `go` pin in `mise.toml`:

- A minor-only floor (`go 1.26`) lets CI float to the latest 1.26.x, drifting ahead of the pinned dev toolchain.
- A `.0` floor (`go 1.26.0`) pins CI to the oldest 1.26 patch, drifting behind it.
- The exact patch (`go 1.26.4`) makes CI install the same Go developers run. This is also what `go mod init`/`go mod tidy` write by default.

Bump `go.mod` and `mise.toml` together on a Go upgrade. The coupling now carries a second consequence: `govulncheck` reports standard-library advisories against the Go on `PATH`, so whichever pin a given run resolves decides what the vulnerability scan considers vulnerable. CI's scan job uses `setup-go` from `go.mod`, a developer's `just vuln` uses mise's — they agree only while the two pins do.

## mise.lock moves with every mise.toml pin

`mise.toml` sets `lockfile = true`, and `mise.lock` records each tool's resolved URL and checksum per platform. It is committed, and the two files must move together: run `mise lock` and commit the result in the same commit as any `mise.toml` version change.

What a stale lock does depends on where it runs, and the difference is the trap. **In CI** it fails loudly — `jdx/mise-action` runs `mise install --locked`, which refuses any tool lacking a recorded URL for the runner's platform. **Locally** it fails silently in the other direction: `mise install` updates an existing lockfile in place, so a bump followed by an install quietly rewrites `mise.lock` and hands you a diff you did not ask for. Review that diff rather than assuming your install could not have touched it. (`mise lock` is what *creates* the lockfile; `mise install` only maintains one that exists.)

Two more facts worth knowing before editing either file:

- A platform absent from the lock gets written in by whoever first installs on it, so an install from a new platform also produces a diff to review.
- `mdbook-linkcheck2` publishes an `x86_64-unknown-linux-gnu` asset and nothing else, so its lock entry covers the linux platforms only. That is the artifact's own limit, not an incomplete lock.

## The mise version is one atomic value across both workflows

`jdx/mise-action`'s `version:` is pinned in `.github/workflows/ci.yml` (docs job) and `.github/workflows/vuln.yml` (toolchain-report job). Keep them identical: mise is the component that verifies every other tool against `mise.lock`, so a floating mise means a floating verifier, and two different mises mean two different verifiers.

This pin is also the toolchain component with the *least* automated coverage in the repository. Dependabot's `github-actions` ecosystem updates the `jdx/mise-action@v4` tag but never reads the action's `version:` input; `mise outdated` reads `mise.toml`, where mise itself does not appear. Nothing reports it — it moves only when a human moves it, which is why it belongs in the manual review posture recorded in `SECURITY.md`.

## The govulncheck tool dependency reaches the built binary

`govulncheck` is a `go.mod` tool dependency, so its requirements participate in the main module's version resolution. That is not confined to tooling: adding it raised `golang.org/x/sys` (via `golang.org/x/telemetry`), and `golang.org/x/sys/cpu` is linked into `cmd/overstory`. Every future update of `x/vuln` or its dependencies can do the same.

The consequence for review: a bump that looks like tooling can change the shipped binary's build list. Check `go list -deps ./cmd/overstory` when a govulncheck update moves a shared dependency, and do not describe such a change as CI-only without checking.

## mdbook and mdbook-linkcheck2 versions move together

`mdbook-linkcheck2` is an mdbook renderer backend pinned in `mise.toml` alongside `mdbook`. It links against mdbook's library crates (`mdbook-driver`/`mdbook-renderer`), so a backend built against a different mdbook minor can fail to parse the book mdbook produces — `mdbook build` (and with it the CI docs job and the pre-push docs hook) would error. Both pins live in `mise.toml`, not split across files; bump them in the same commit, and confirm the chosen `mdbook-linkcheck2` release supports the target mdbook version before upgrading.

## pre-commit formats with CI's formatter set

CI's golangci-lint enforces the formatters configured in `.golangci.yml` (`gofmt` + `goimports`). The pre-commit hook in `lefthook.yml` runs `golangci-lint fmt`, which applies that same configured set and re-stages the result — so an import-ordering issue can't pass the commit hook only to fail the lint job later. A hook that runs only `gofmt` would leave that gap. If the formatters enabled in `.golangci.yml` change, the hook inherits them automatically; no second edit is needed.

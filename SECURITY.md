# Security Policy

## Reporting a vulnerability

Please report security issues privately through GitHub's [private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability) on this repository's **Security** tab, rather than opening a public issue.

If that route is unavailable to you for any reason, open a regular issue saying only that you have a security report and how to reach you — no details — and you'll get a private channel back. Never put the details in a public issue.

Include what you did, what happened, and what you expected — a reproduction is the most useful thing you can send. Please allow time for a fix before disclosing publicly.

## Supported versions

Overstory is in early development and cuts no tagged releases yet, so the supported surface is the current `main` branch. Fixes land there; there is no backport channel.

## Credential model

Overstory does not manage credentials of its own. It authenticates to GitHub by shelling out to `gh auth token` once per process, inheriting whatever authentication the operator's [`gh`](https://cli.github.com/) CLI already holds. Consequences worth knowing:

- **The server's access equals the operator's `gh` access.** It can read any repository that token can read, including private ones. It never widens that scope, and it requests no scopes of its own.
- **The token is held in memory only** — fetched lazily on first use and, once obtained, cached for the life of the process. Nothing is written to disk, and no token is embedded in a manifest or config file.
- **The token is never logged and never appears in a returned error.** When `gh` fails, the error is classified (not installed, or not authenticated) without echoing the subprocess's stderr, which can carry sensitive detail.
- **The server is read-only against GitHub.** It issues queries; it does not create, edit, close, or label anything.

## Build and CI supply chain

Overstory runs with the operator's GitHub credentials, so anything that executes during its build or in its CI runs in reach of them. What follows is what the project actually does about that — no more.

- **Dependencies and the standard library are scanned for known vulnerabilities.** [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) runs on every pull request, on pushes to `main`, and weekly on a schedule. It reports advisories whose vulnerable symbols are reachable from this code. The scan is **advisory, not blocking** — it is not a required status check, so it informs review rather than gating merges. Note also that GitHub disables scheduled workflows after 60 days without repository activity: a gap in the weekly runs means the schedule stopped, not that nothing was found.
- **Build-time dependencies are checksum-verified.** Go modules are verified against the checksum database and `go.sum`, re-hashed in CI by `go mod verify`, and held tidy by `go mod tidy -diff` so entries no longer required cannot linger. `govulncheck` itself is a `go.mod` tool dependency, so the scanner is covered by the same verification as everything else.
- **The CI toolchain is pinned by checksum.** Tool versions live in `mise.toml` and their resolved download URLs and checksums in the committed `mise.lock`; CI installs with `mise install --locked`. This matters most for the documentation build, which executes a prebuilt binary from a third-party repository — the lockfile is what makes that the exact reviewed artifact rather than whatever a tag currently points at. Verification happens **at download**: CI caches the installed tool directory, so a cache hit restores previously verified bytes without re-checking them.
- **Update automation covers Go modules and GitHub Actions.** Dependabot watches both weekly. Actions are pinned by tag (`@v4`), not by commit digest.
- **The mise toolchain is reviewed manually.** No update bot covers `mise.toml`, so the weekly scan run also reports outdated pins to its run summary, and that report — along with any CI toolchain failure, or preparing a release — is what triggers a review. `just toolchain-outdated` runs the same check locally. The pinned version of mise itself, which lives in the workflows rather than in `mise.toml`, is reviewed the same way and is reported by nothing.

This describes a detector paired with a human response, not a project that is continuously free of known advisories. Go patch releases routinely fix reachable standard-library symbols, so the scan going red is expected periodically and is resolved by a maintainer bumping the pinned toolchain.

## Data handling

Overstory speaks MCP over stdio and runs as a subprocess of the calling agent. It stores nothing between runs, opens no network listener, and sends repository data nowhere except back to the caller that asked for it. Diagnostics go to stderr; stdout carries only the JSON-RPC protocol stream.

Repository conventions come from operator-supplied manifests read from the local filesystem. Because manifest keys are repository names, a manifest can itself be sensitive metadata — the Manifests page of the documentation book describes the layering approach that keeps private repository names out of committed configuration.

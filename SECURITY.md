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

## Data handling

Overstory speaks MCP over stdio and runs as a subprocess of the calling agent. It stores nothing between runs, opens no network listener, and sends repository data nowhere except back to the caller that asked for it. Diagnostics go to stderr; stdout carries only the JSON-RPC protocol stream.

Repository conventions come from operator-supplied manifests read from the local filesystem. Because manifest keys are repository names, a manifest can itself be sensitive metadata — the Manifests page of the documentation book describes the layering approach that keeps private repository names out of committed configuration.

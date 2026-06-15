# Installation & Registration

A linear path from a cloned repository to a registered MCP server an agent can call. Overstory is a single, daemonless binary: it serves one session over stdio and exits. There is no background process and no network service to manage.

## Prerequisites

- **The [`gh`](https://cli.github.com/) CLI, authenticated.** Overstory fetches issues and PRs from the GitHub GraphQL API in-process, using credentials it sources from `gh` (`gh auth token`). It inherits your existing `gh` authentication — there is no separate token to configure. Run `gh auth status` to confirm you're logged in.
- **A toolchain to build the binary** — [mise](https://mise.jdx.dev/) and [just](https://github.com/casey/just), per the repository's `CONTRIBUTING.md`. (Prebuilt binaries are not yet distributed; you build from source.)

## Install the binary

From the repository root:

```sh
mise install     # provision the pinned toolchain (Go, just, ...)
just install     # build and install to ~/.local/bin/overstory
```

`just install` builds the binary and installs it to `~/.local/bin/overstory`. Ensure `~/.local/bin` is on your `PATH`. To build without installing, `just build` writes the binary to `bin/overstory`.

## Register it as an MCP server

The command an agent runs is the bare binary — **no arguments and no flags**. All configuration comes from the environment (manifest discovery, covered in [Manifests](../manifest.md)) and from `gh` (authentication). A minimal MCP server entry looks like:

```json
{
  "mcpServers": {
    "overstory": {
      "command": "overstory"
    }
  }
}
```

Place this where your agent reads MCP server configuration — for example, a project's `.mcp.json`, or your client's MCP settings. With Claude Code you can register it directly:

```sh
claude mcp add overstory overstory
```

If overstory should survey repositories whose conventions live in a manifest file outside the default discovery directory, pass that location through the `OVERSTORY_MANIFESTS` environment variable in the server entry's environment — see [Manifests](../manifest.md) for discovery rules.

> The server spawns at session start and reads its environment then. After installing or registering it — or changing its environment — restart the agent session so the new server is picked up.

## Confirm it works

Once registered, the agent exposes two tools, `backlog_review` and `project_summary`, each taking an `owner` and `repo`. Call either against a repository you can read with `gh`; it returns structured facts (see [Tools & Facts](../tools.md)). No tool modifies anything — both are read-only surveys.

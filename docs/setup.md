# Share skills and MCPs across agents

AgentBridge (`agb`) can apply a named setup profile to OpenCode, Claude Code, and Codex
accounts. Define a capability once, choose its targets, then run `agb sync`.
Existing session-picker commands continue to work.

## Start with Cloudflare documentation

Print a starter block to merge into your existing `agb` configuration:

```bash
agb setup --example
```

The default configuration path is `~/.config/agentbridge/config.json`. Use your existing
account names in `targets`; without configured accounts they are `oc`, `cc`, and
`cx`. For example:

```json
{
  "$schema": "https://raw.githubusercontent.com/sergio-gimenez/opencode-sessions/main/docs/setup.schema.json",
  "setup": {
    "version": 1,
    "profiles": {
      "development": {
        "targets": ["oc", "cc", "cx"],
        "mcps": {
          "cloudflare-docs": {
            "transport": "http",
            "url": "https://docs.mcp.cloudflare.com/mcp"
          }
        }
      }
    }
  }
}
```

The schema is shipped in [setup.schema.json](setup.schema.json); the remote URL
becomes available when this version is published. The endpoint comes from
[Cloudflare's official MCP catalog](https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/).
Any compatible HTTP or stdio MCP can be declared in the same way.

Review and apply:

```bash
agb plan --profile development
agb sync --profile development
agb sync --profile development --check
```

If only one profile exists, `--profile` is optional. `--config /path/config.json`
selects a different manifest. `--target cx` limits the operation to a target
already listed in that profile. `agb plan`, `agb sync --dry-run`, and
`agb sync --check` do not write files. `AGB_DRY_RUN=1` also prevents sync writes.

`--check` exits with 0 for an unchanged setup, 1 for pending changes, and 2 for
conflicts or invalid configuration. A normal successful sync exits with 0.
`--json` emits the plan as machine-readable metadata and statuses, without
environment values or native configuration contents. A sync still applies its
plan when `--json` is used without `--check` or `--dry-run`.

## Share an existing local skill

Add a `skills` map alongside `mcps` in the profile:

```json
"skills": {
  "cloudflare": { "path": "~/.agents/skills/cloudflare" }
}
```

The source must already exist and contain `SKILL.md`. `agb` links the whole
folder, so references, scripts, and other supporting files travel with it.
Relative paths resolve from the manifest's directory. Updating the source
updates all links; a sync does not download repositories or run installers.

Default destinations are:

| Target | MCP config | Skills directory |
| --- | --- | --- |
| OpenCode | `~/.config/opencode/opencode.json` or existing `opencode.jsonc` | `~/.agents/skills` |
| Default Claude account | `~/.claude.json` | `~/.claude/skills` |
| Other Claude account | `<configDir>/.claude.json` | `<configDir>/skills` |
| Default Codex account | `~/.codex/config.toml` | `~/.agents/skills` |
| Other Codex account | `<codexHome>/config.toml` | `~/.agents/skills` |

OpenCode's config location honors `XDG_CONFIG_HOME` and `OPENCODE_CONFIG`.
Explicit Codex accounts use their configured `codexHome`; the default destination
does not inherit an unrelated account's `CODEX_HOME` from the shell.

Codex and OpenCode document the shared `.agents/skills` discovery location.
This directory is user-wide: other local tools or accounts may also discover
skills placed there. `agb` reports this in the plan and rejects contradictory
definitions or exclusions at the same physical destination. Per-account skill
isolation is not provided by changing a profile's target list alone.
[OpenAI Docs for skills](https://learn.chatgpt.com/docs/build-skills),
[OpenCode skills](https://opencode.ai/docs/skills/).

Custom locations can be set per target under `setup.destinations`:

```json
"destinations": {
  "cc": {
    "mcpConfig": "/custom/claude/.claude.json",
    "skillsDir": "/custom/claude/skills"
  }
}
```

These specify files and folders to manage; they do not change where the native
agent searches. Choose a destination actually loaded by that agent. OpenCode
also searches Claude-compatible directories, so existing duplicate skill names
can remain visible through other search paths. Agent-specific tool names,
frontmatter, and plugins may require adaptation; links preserve their contents.

## Exceptions and credentials

Use `overrides` in a profile to amend selected capabilities for one target:

```json
"overrides": {
  "cx": {
    "mcps": {
      "cloudflare-docs": { "enabled": false }
    }
  }
}
```

Disabling an entry removes an `agb`-managed registration or link; it never
removes the source skill. It cannot delete an unmanaged installation. Removing
an item from the manifest leaves its previous installation in place; automatic
pruning and adoption of unmanaged entries are future features.

For a local server, use a command and an argument array:

```json
"local-tool": {
  "transport": "stdio",
  "command": "node",
  "args": ["/path/to/server.js"],
  "envVars": ["SERVICE_TOKEN"]
}
```

`envVars` forwards variables by name using each tool's native mechanism. An
HTTP server can instead specify `bearerTokenEnv: "SERVICE_TOKEN"`. The manifest
and generated configs reference the variable; `agb` does not read or copy its
value. OAuth login remains separate for each tool/account. Defining an MCP does
not perform authentication or confirm that the server is reachable.
Native configuration formats are documented by
[Claude Code](https://code.claude.com/docs/en/mcp),
[OpenAI Docs for MCP](https://learn.chatgpt.com/docs/extend/mcp?surface=cli), and
[OpenCode](https://opencode.ai/docs/mcp-servers/).

## Existing configurations and agent-driven use

The planner distinguishes additions, updates, removals, existing equivalent
entries, shared destinations, disabled entries, and conflicts. Equivalent manual
MCP entries retain their native options and remain unmanaged. A different
definition under the same unmanaged name is a conflict. Choose another name or
reconcile that entry manually before syncing.

`agb` stores ownership fingerprints in `sync-state.json` beside its configuration
file. It checks those fingerprints to detect manual edits to managed resources.
Native config edits preserve unrelated settings and comments. Config symlinks
are followed so dotfile-manager links remain in place. Modified files get
private backups under `sync-backups/` in the same directory. Files are replaced
atomically, and a failed apply attempts to roll back completed registrations.
Keep the state file when moving your manifest; it records which entries belong
to AgentBridge. Managed TOML blocks have `AgentBridge MCP begin/end` comments
used for updates; blocks written by pre-AgentBridge versions remain supported.

An agent can configure this through the same CLI, with no embedded model in
`agb`. A useful request is:

> Read my agb account configuration. Add a development profile that shares my
> local Cloudflare skills and the documentation MCP across those accounts. Use
> `agb plan --json` to check the destinations and conflicts, then synchronize
> that profile. Preserve my existing tool-specific settings.

The current MVP manages local skills and stdio/HTTP MCP definitions. Guided
inventory/import, remote skill downloads, project profiles, per-account skill
disable adapters, readiness checks, and a bundled setup skill remain on the
[design roadmap](shared-agent-setup.md).

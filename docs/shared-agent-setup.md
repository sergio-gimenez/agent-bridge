# Shared agent setup and product direction

Status: product design, October 2026. AgentBridge now implements named
profiles, local skill links, stdio/HTTP MCP definitions, planning, and explicit
sync. See [the working setup guide](setup.md). Inventory/import, guided setup,
readiness checks, and launch integration below remain proposed interfaces.
Session handoffs and shared setup use the `agb` command.

The project should let someone choose a coding agent without rebuilding their
working environment. It combines finding and carrying conversations with
keeping selected skills and MCP connections available across tools and accounts.
Each user defines what should be shared, where it applies, and any exceptions.

## Define the setup once

Add an optional `setup` block to the existing configuration. Existing account
names remain the target identifiers, including `oc` for OpenCode. Named profiles
group capabilities for a purpose, such as personal development or work.

This illustrative profile shares an existing local skill and Cloudflare's
documentation MCP across three configured targets:

```json
{
  "claudeAccounts": [{ "name": "cc1" }],
  "codexAccounts": [{ "name": "cx1" }],
  "setup": {
    "version": 1,
    "profiles": {
      "development": {
        "targets": ["oc", "cc1", "cx1"],
        "skills": {
          "cloudflare": {
            "path": "~/.agents/skills/cloudflare"
          }
        },
        "mcps": {
          "cloudflare-docs": {
            "transport": "http",
            "url": "https://docs.mcp.cloudflare.com/mcp"
          }
        },
        "overrides": {
          "cx1": {
            "mcps": {
              "cloudflare-docs": { "enabled": false }
            }
          }
        }
      }
    }
  }
}
```

Remove the override to enable the MCP on every listed target. The endpoint is
listed in [Cloudflare's official MCP catalog](https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/).
Cloudflare-specific defaults belong in an optional preset; the manifest accepts
any skill source or MCP endpoint.

Start with local skill folders, stdio servers specified as command/argument
arrays, and remote HTTP servers. Skills include their supporting files, not just
`SKILL.md`. A subsequent source resolver can support Git repositories with a
pinned revision and selected subdirectory. Sources resolve relative to the
manifest location, with explicit home expansion.

Resolve profile defaults followed by target overrides. Report a conflict if two
selected profiles assign different definitions to the same target/resource key;
profile order must not silently decide the result. Keep project and user scope
explicit. A future project manifest can refine a selected user profile, with its
effective values shown in the plan.

## Discover, plan, apply, diagnose

The current and proposed command surface uses `agb`:

```text
agb setup                         guided profile creation
agb inspect --json                discover installed capabilities and origins
agb import --from cc1              draft a profile from selected existing entries
agb plan --profile development     show the exact changes and conflicts
agb sync --profile development     apply the declared setup
agb sync --check                   detect drift without writing
agb doctor --profile development   check configuration, discovery, and readiness
```

An import previews available entries for selection. It retains unknown native
fields as target-specific overrides and excludes credentials from the shared
manifest. A generated profile is ordinary editable configuration.

The plan distinguishes add, update, already configured, conflict, unsupported,
and authentication required. An equal native entry needs no duplicate. Adoption
into the managed set is a separate explicit selection. A conflicting unmanaged
entry remains a conflict until the user chooses which definition should win.
`sync` applies requested, supported changes without an extra confirmation for
each entry; unresolved conflicts are reported before writes begin.

Record ownership, resolved source revisions, and the last applied values in a
persistent state directory separate from the disposable session index. Before
updating an entry, compare its current value with the last applied value: manual
edits are drift that requires a documented resolution, not permission to replace
the entry. Modify only owned entries and selected skill links. Removal is a
separate explicit prune operation limited to owned resources.

Preserve unrelated settings and comments in JSONC/TOML. Prepare and validate all
changes before writing; use atomic replacement per file, private backups, and
preconditions that detect concurrent edits. Cross-file updates are not globally
atomic: report completed operations and retain enough state to roll back or
resume safely. Re-running an unchanged sync should produce no changes.

## Translate capabilities through tool adapters

Use an adapter per tool with discovery, capability reporting, planning, apply,
and readiness checks. Account paths come from the existing account registry;
explicit destination overrides accommodate nonstandard installations. Native
CLI commands may help manage configuration, but the plan must describe the
actual files/entries they will change.

| Tool | MCP representation | Skills baseline |
| --- | --- | --- |
| Claude Code | JSON `mcpServers`; HTTP uses an explicit transport type | Native personal/project skill folders |
| Codex | TOML `[mcp_servers.<name>]`; command or URL | Documented `.agents/skills` discovery and supported links |
| OpenCode | JSON/JSONC `mcp`; `local` command array or `remote` URL | Native and compatible skill folders |

These formats come from [Claude Code's MCP docs](https://code.claude.com/docs/en/mcp),
[OpenAI Docs for MCP](https://learn.chatgpt.com/docs/extend/mcp?surface=cli), and
[OpenCode's MCP docs](https://opencode.ai/docs/mcp-servers/). Verify the installed
tool version and destination resolution when implementing an adapter.

Codex and OpenCode both document `~/.agents/skills` discovery. Prefer an existing
shared skill source where it provides the desired scope; link into a native
directory where needed. Avoid creating multiple discoveries of the same skill.
[OpenAI Docs for skills](https://learn.chatgpt.com/docs/build-skills),
[OpenCode skills](https://opencode.ai/docs/skills/), and
[Claude Code skills](https://code.claude.com/docs/en/skills) describe the different
search paths and behavior.

Shared discovery creates a scope constraint: installing into a user-wide
directory can expose a skill to other tools or accounts using that directory.
The plan must show this wider exposure. A target exclusion may require a native
disable setting or isolated installation; an adapter must report when the
requested isolation is unsupported. Do not assume changing an account home
changes every skill search path. Tool-specific frontmatter, hooks, or bundled
plugins also need explicit compatibility treatment; copying files alone does
not establish equivalent behavior.

Synchronize MCP definitions and credential references. Keep each tool/account's
OAuth sessions in its native credential store and report any required login.
Support environment-based secrets through each adapter's native mechanisms,
with redacted output. Matching endpoint configuration does not imply that a
server is authenticated or that its tools are available. `doctor` reports those
states separately; it does not exercise deployment or write tools to test them.

## Let an agent help define the setup

A user should be able to ask their current coding agent:

> Give Claude, Codex, and OpenCode my Cloudflare skills and documentation MCP.
> Keep the work account's setup separate.

The agent can inspect installations through `agb inspect --json`, draft or edit
the manifest, resolve requested exceptions, and run the same planner and sync
commands a human would use. Profiles persist independently of that conversation.
There is no requirement for a second model subscription inside `agb`.

First expose stable CLI commands, JSON output, exit statuses, and a JSON schema
for the manifest. Then package an optional setup skill that teaches this
workflow. An MCP interface for `agb` itself can follow if it improves integration;
both interfaces should call the same configuration engine.

The default launch flow continues to open sessions. Capability mismatch can be
shown in the session card or through an explicit preflight check. An eventual
automatic sync mode must be selected per profile and restricted to its owned
resources. Switching a session should not silently import that source agent's
entire setup into another account.

## Delivery order

1. Inventory and schema: discover MCPs and local skills for the existing account
   registry, normalize supported fields, and produce a read-only plan.
2. First sync: local skills plus stdio/HTTP MCP definitions, explicit target
   selection, ownership records, private backups, drift/conflict detection, and
   idempotent apply across the three adapters.
3. Easy configuration: import selection, named presets, and an agent setup skill
   built on the JSON CLI. Add pinned repository sources and native auth guidance.
4. Project profiles and launch preflight: account exceptions, wider discovery
   reporting, and capability checks when carrying a conversation.

Acceptance cases include adding Cloudflare tooling to all three tools, disabling
one target, keeping work and personal destinations distinct, preserving native
comments/settings, detecting a manually changed managed entry, a no-op second
sync, and recovering from a partially completed apply. Test these against
temporary homes and stubbed CLIs, with separate opt-in native-version checks.

## Naming

The product is **AgentBridge**, with `agb` as the CLI. “Bridge” describes both
parts of the product directly: carrying a conversation between agents and
making a selected toolset available on both sides. It is vendor-neutral and
still makes sense as synchronization grows beyond sessions.

Several attractive names already have closely related projects:
[AgentLoom](https://github.com/nstfn/agent-loom) manages skills,
[Shuttle](https://github.com/glazec/shuttle-releases) manages skills and MCPs,
and [Concord](https://www.npmjs.com/package/@malleus35/concord) describes
cross-harness asset synchronization. These also provide useful comparisons for
the inventory and sync experience.

AgentBridge uses `agb`, `~/.config/agentbridge`, `~/.cache/agentbridge`, and
`AGB_*`. Older configuration and environment names remain readable only to make
migration safe; the installer removes obsolete command aliases. The
repository/module URL can move separately without forcing users to reconstruct
their setup.

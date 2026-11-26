# Configuration and internals

Everything `agb` reads, where it keeps its own state, and the settings that
change how it launches a session.

## The config file

`agb` works with no config at all: one account per tool, each in its default
home. Everything else goes in `~/.config/agentbridge/config.json` (or wherever
`AGB_CONFIG_PATH` points). Every key is optional.

| Key | What it sets | Details |
| --- | --- | --- |
| `claudeAccounts` | Claude accounts: `name`, optional `configDir`, optional `skipPermissions` | [Claude Code](agents/claude-code.md) |
| `defaultClaudeAccount` | Where a route that reaches Claude without naming an account lands | [Claude Code](agents/claude-code.md#3-configure-agb) |
| `codexAccounts` | Codex accounts: `name`, optional `codexHome`, optional `skipPermissions` | [Codex](agents/codex.md) |
| `defaultCodexAccount` | The same, for Codex | [Codex](agents/codex.md) |
| `opencode` | The default OpenCode store's `skipPermissions` | [OpenCode](agents/opencode.md) |
| `opencodeAccounts` | Wrapper stores: `name`, `command`, `dbPath`, optional `configHome` | [OpenCode](agents/opencode.md#wrappers-with-their-own-store) |
| `antigravity` | `false` to hide Antigravity, or `{ "skipPermissions": true }` | [Antigravity](agents/antigravity.md#configuration) |
| `skipPermissions` | The global YOLO default | [Permissions](#permissions) |
| `moveHosts`, `arrive` | Machines for push and pull, and what runs when a session lands | [Push and pull](push-pull.md) |
| `secret` | Models a secret conversation may or may not use | [Secret conversations](secret.md) |
| `setup` | Profiles of skills, MCPs and memory for `agb sync` | [Share skills and MCPs](setup.md) |

A config with two Claude accounts and two Codex accounts looks like this:

```json
{
  "claudeAccounts": [
    { "name": "cc1" },
    { "name": "cc2", "configDir": "~/.claude-cc2" }
  ],
  "defaultClaudeAccount": "cc1",
  "codexAccounts": [
    { "name": "cx1" },
    { "name": "cx2", "codexHome": "~/.codex-cx2" }
  ],
  "defaultCodexAccount": "cx1"
}
```

An account without `configDir` or `codexHome` is the tool's own default home.
Leave a whole block out and `agb` falls back to that default, labelled `[CC]` or
`[CX]`. Names share one list of targets across tools, so they must be unique;
they become the row labels and what `--target` takes:

```bash
agb --target cx2            # any account name, or "oc" for OpenCode
agb --claude-account cc2    # the same thing, said the long way
agb --codex-account cx2
```

`agb` records which account owns each session from the store it was scanned
in, so a `cc1` session resumes in `cc1` unless you deliberately retarget it.

## Permissions

Launch the target tool with permission checks bypassed
(`claude --dangerously-skip-permissions`, `opencode --auto`,
`codex --dangerously-bypass-approvals-and-sandbox`,
`agy --dangerously-skip-permissions`):

```bash
agb --dangerous     # also --skip-permissions / --yolo
agb --safe          # force checks back on, overriding the config default
```

To go yolo just once, press `Ctrl+Y` in the picker. The status line turns into a
red `YOLO` for the launch you are about to make; press it again to go back to
asking. Nothing is saved.

Defaults live in `~/.config/agentbridge/config.json`, globally or per agent:

```json
{
  "skipPermissions": false,
  "opencode": { "skipPermissions": true },
  "claudeAccounts": [
    { "name": "cc1" },
    { "name": "cc2", "configDir": "~/.claude-cc2", "skipPermissions": true }
  ],
  "codexAccounts": [{ "name": "cx1", "skipPermissions": true }],
  "antigravity": { "skipPermissions": false }
}
```

What wins, strongest first:

```
Ctrl+Y in the picker  →  --yolo / --safe  →  the target's skipPermissions  →  global skipPermissions
```

The setting follows the *target*, not the session: forking a `cc1` session into
`cx1` uses `cx1`'s default.

> **Note**
> Skipping permission checks lets the agent act on your filesystem without
> prompting. Turn it on only for directories where you accept that.

## Subscription suggestions

The header suggests the configured subscription with the most headroom.
AgentBridge compares the least remaining capacity across each account's rolling
windows, ignores blocked and unknown accounts, and keeps configuration order as
the tie-breaker. This is advice only: `Enter` still resumes a session in its own
account unless you pin another target. `Ctrl+G` pins the suggestion.

Claude Code limits come from the account's OAuth usage endpoint; Codex limits
come through its local app-server protocol. Each configured account is checked
with its own `CLAUDE_CONFIG_DIR` or `CODEX_HOME`. OpenCode is provider-agnostic,
so it remains `unknown` until it has a provider-neutral usage interface.
Antigravity is `unknown` too: `agy` shows its quota only through `/usage`
inside its own TUI.
These are CLI-owned interfaces rather than a shared industry API; an older or
changed client degrades to `unknown` or the last cached observation.

Checks run after the picker's first frame and never delay opening it. A
two-minute snapshot lives in `~/.cache/agentbridge/limits.json` with mode `0600`.
It contains target names, percentages and reset times only, never access or
refresh tokens. If a refresh fails, the previous observation remains available
with its original timestamp. `agb limits --json` exposes the same
provider-neutral report so external tools do not need to read AgentBridge's
configuration or know provider APIs.

## Dry run

Set `AGB_DRY_RUN=1` and `agb` prints what it *would* launch (the command, the
account, the working directory) instead of launching it. Handy for checking how
a route resolves, and for bug reports:

```console
$ AGB_DRY_RUN=1 agb
would run CODEX_HOME=~/.codex-cx1 codex <transcript seed, 1655 chars>
  in ~/code/nebula-api
```

## Where the data comes from

`agb` reads each tool's existing store and never writes to them.

| Source | Path | Override |
| --- | --- | --- |
| OpenCode | `~/.local/share/opencode/opencode.db` (SQLite) | `OPENCODE_DB_PATH` |
| Claude Code | `~/.claude/projects/**/*.jsonl`, plus each account's `configDir` | `CLAUDE_PROJECTS_PATH` |
| Codex | `~/.codex/sessions/**/rollout-*.jsonl`, plus each account's `codexHome` | `CODEX_HOME`, `CODEX_SESSIONS_PATH` |
| Antigravity | `~/.gemini/antigravity-cli/brain/*/.system_generated/logs/transcript.jsonl`, with titles and directories from `conversation_summaries.db` (SQLite, read-only) | none |
| `agb` config | `~/.config/agentbridge/config.json` | `AGB_CONFIG_PATH` |
| `agb` cache | `~/.cache/agentbridge/index.gob` | `AGB_CACHE_PATH`, `XDG_CACHE_HOME` |
| Picker layout | `~/.cache/agentbridge/layout.json`, beside the cache | follows the cache |
| Subscription limits | `~/.cache/agentbridge/limits.json`, beside the cache | follows the cache |
| Fork handoffs | `~/.cache/agentbridge/handoffs/`, beside the cache | follows the cache |

`agb` skips a store that's missing or unreadable instead of dying on it, so not
having OpenCode installed still gets you your Claude and Codex sessions.

Codex files each session as a rollout under `sessions/YYYY/MM/DD/` and archives
one by moving it into `archived_sessions/`, so archived Codex sessions drop out
of the list the way archived OpenCode ones do. Codex stores no title in the
rollout, which is why a Codex row is titled by its opening prompt.

Antigravity writes each conversation's steps as plaintext JSONL and indexes
them in `conversation_summaries.db`. `agb` takes your prompts from the
`<USER_REQUEST>` block of each user step and the replies from the model's text
steps, and skips tool calls and their output. Conversations from the
Antigravity IDE are indexed there too but have no plaintext transcript (their
`.pb` files are opaque), so they are not listed. A conversation started with
`agy -p` has no title until `agy` writes one, so it is titled by its opening
prompt.

## Why it starts fast

Reading every transcript on every launch is what used to make `agb` slow: on a
machine with a few hundred sessions that is hundreds of megabytes of JSONL. Now
`agb` keeps a cache of what each transcript boiled down to and only reads what
changed since the last launch:

```mermaid
flowchart LR
  L[launch] -->|stat every .jsonl| C{size, mtime, inode<br/>vs cache}
  C -->|unchanged| R[cached record]
  C -->|grew, same head and tail bytes| A[parse only the appended lines]
  C -->|new, shrunk or rewritten| F[parse the whole file]
  L -->|list 200 newest sessions| O{time_updated<br/>vs cache}
  O -->|unchanged| R
  O -->|moved| Q[query that session's prompts]
```

Claude Code and Codex only ever append to a transcript, so the session you are
in the middle of, usually the biggest and the only one that changed, costs just
its new lines. The first launch builds the cache and takes as long as a full
read; after that the picker is up in a few tens of milliseconds. The cache is
safe to delete, and `agb --rescan` rebuilds it.

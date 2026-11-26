---
title: OpenCode
description: OpenCode's default store, wrapper commands with stores of their own, and how agb treats OpenCode 1 and 2.
---

# OpenCode

OpenCode keeps every session in one SQLite database, so unlike Claude Code and
Codex it has no per-account home to switch between. `agb` reads that database
(read-only, always) and launches `opencode` to resume or start sessions. The
default store gets the <span class="agb-label agb-label--oc">OC</span> label and
needs no configuration at all.

## Install

```bash
curl -fsSL https://opencode.ai/v2/install | bash
opencode auth login
```

That installs OpenCode 2. The [OpenCode docs](https://opencode.ai/docs) list
Homebrew, npm and the rest. `opencode auth login` connects a model provider;
OpenCode works with many, which is also why `agb` can't show limit bars for it.
There's no single subscription to measure.

Have one conversation, then run `agb`. OpenCode rows appear straight away.

## OpenCode 1 and 2

`agb` works with both. It decides which one it's talking to from the
database itself (OpenCode 2 adds a `session_v2` table), not from the binary,
and a migrated database that still holds the old tables is read in full.

The two CLIs behave differently, so routes differ too:

| | OpenCode 1 | OpenCode 2 |
| --- | --- | --- |
| Resume | `opencode --session <id>` | `opencode --session <id>` |
| Fork (`Ctrl+F`) | `opencode --session <id> --fork` | seeded fork, since the TUI has no `--fork` |
| Seeded session from another tool | `opencode run --dir <dir> "<seed>"` | `opencode --prompt "<seed>" <dir>`, which lands the seed in the composer |
| Transcript export for a handoff | `opencode export <id>` | `opencode session export <id>` |
| YOLO | `--auto` | `--auto` |

With OpenCode 2 the seed sits in the input box rather than being sent for you;
press `Enter` to send it.

## Wrappers with their own store

Some setups run a second OpenCode with separate config and data, say a work
profile behind a wrapper script that sets `XDG_DATA_HOME` and
`XDG_CONFIG_HOME`. `agb` can list and launch that store as its own target, next
to the default `oc`:

```json
{
  "opencodeAccounts": [
    {
      "name": "opencode-work",
      "command": "opencode-work",
      "dbPath": "~/.local/share/opencode-work/opencode/opencode.db",
      "configHome": "~/.config/opencode-work"
    }
  ]
}
```

`command` is the wrapper on your `PATH`, or an absolute path; it's responsible
for its own environment, and `agb` just runs it with the same arguments it
would give `opencode`. `dbPath` is the store to read. `configHome` is optional
and only matters for [`agb sync`](../setup.md), which writes MCP servers into
`<configHome>/opencode/opencode.json`.

Within one store, `Enter` resumes through that store's wrapper with the
original id. Crossing between `oc` and `opencode-work` is a seeded fork,
exactly like crossing between two Claude accounts, because the session id
means nothing to the other store. Each store keeps its own slice of `agb`'s
cache.

```bash
agb --target opencode-work
agb new --target opencode-work --dir ~/src/client-app
```

## Secret conversations

OpenCode is the agent behind `agb`'s secret conversations: `Ctrl+N`, then
`Ctrl+S`, starts OpenCode from a throwaway store in RAM with no plugins or MCPs,
limited to local models and ones OpenCode Go lists with zero retention, and
deletes the lot on exit. That has [its own page](../secret.md).

## What doesn't work

Push and pull don't move OpenCode sessions, since a session is rows in a shared
database rather than a file that can be copied whole. Herdr's conversation
restore doesn't cover OpenCode either. Archived OpenCode sessions are left out
of the list on purpose.

## Environment

| Variable | Effect |
| --- | --- |
| `OPENCODE_DB_PATH` | Read this database instead of `~/.local/share/opencode/opencode.db` |
| `OPENCODE_CONFIG` | Where `agb sync` writes the default store's MCP config |
| `XDG_CONFIG_HOME` | Root for the default store's config when `OPENCODE_CONFIG` isn't set |

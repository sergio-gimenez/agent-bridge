---
title: Scripting agb
description: The JSON agb prints for launchers and scripts, how to open sessions without the picker, and the hooks and environment variables it honours.
---

# Scripting agb

Everything the picker does has a command behind it, and two of them print JSON
meant for other programs. A launcher, a status bar or a shell function can use
`agb` without knowing where any agent keeps its files.

## List sessions

```bash
agb --print --json
```

prints up to 200 of the newest sessions across every store as one JSON array,
newest first. (Plain `agb --print` shows 25, as text.) `--query` only pre-fills
the interactive picker, so filter the JSON yourself:

```bash
agb --print --json | jq -r '.[] | select(.prompts | tostring | test("migration"; "i")) | .id' | head -1
```

Each entry looks like this (values invented):

```json
{
  "id": "eac18b4c-a710-4142-997a-85ecf9aac21d",
  "source": "antigravity",
  "account": "ag",
  "title": "Fix the flaky migration test",
  "directory": "/home/you/src/api",
  "updatedAtMs": 1795694007330.98,
  "prompts": ["the migration test fails one run in five", "try it with -count=20"],
  "assistant": ["It's a race on the shared test database.", "20 of 20 passed."],
  "file": "/home/you/.gemini/antigravity-cli/brain/eac18b4c-…/transcript.jsonl",
  "openPid": 41233
}
```

| Field | Notes |
| --- | --- |
| `source` | `claude`, `codex`, `opencode` or `antigravity` |
| `account` | The configured target name (`cc2`, `cx1`, `oc`, `ag`) |
| `prompts`, `assistant` | The last three of each, whitespace collapsed; omitted when empty |
| `file` | The transcript on disk; empty for OpenCode, which keeps sessions in SQLite |
| `openPid` | The process that has the session open on this machine right now, if any |

`agb pull` reads exactly this output from the other machine over SSH, so the
shape stays stable.

## Read limits

```bash
agb limits --json            # cached for two minutes
agb limits --json --refresh  # ask the providers again
```

```json
{
  "version": 1,
  "generatedAt": "2026-11-26T12:05:28Z",
  "recommended": "cc3",
  "accounts": [
    {
      "target": "cc1",
      "source": "claude",
      "status": "available",
      "plan": "pro",
      "windows": [
        { "name": "5h", "usedPercent": 96, "resetsAt": "2026-11-26T12:50:00+00:00" },
        { "name": "7d", "usedPercent": 68, "resetsAt": "2026-11-29T06:00:00+00:00" }
      ],
      "remainingPercent": 4,
      "observedAt": "2026-11-26T12:05:28Z"
    },
    {
      "target": "ag",
      "source": "antigravity",
      "status": "unknown",
      "detail": "provider does not expose subscription limits"
    }
  ]
}
```

`remainingPercent` is the headroom in the *tightest* window, so the 4 above
comes from the 5h window even though the week still has a third left.
`recommended` is the target with the most of it; ties go to config order.
Accounts appear in the same order as `Ctrl+T` lists them, and the report never
contains tokens, endpoints or raw provider responses. Check `version` before
parsing; it changes only when the shape does.

A one-liner for a status bar:

```bash
agb limits --json | jq -r '.accounts[] | select(.remainingPercent) | "\(.target) \(.remainingPercent|floor)%"'
```

## Open without the picker

```bash
agb open eac18b4c                   # resume; an id prefix is enough when it's unique
agb open eac18b4c --target cc2      # carry it to another account as a seeded fork
agb open eac18b4c --fork            # branch instead of continuing
agb new --target ag --dir ~/src/api # blank conversation
```

`--dangerous` and `--safe` set the permission mode for that launch. `agb open`
replaces itself with the agent (it `exec`s), so the agent owns the terminal and
its exit code becomes yours.

## Dry runs

`AGB_DRY_RUN=1` makes every launch print what it would run instead:

```console
$ AGB_DRY_RUN=1 agb open eac18b4c --target cc3
would run CLAUDE_CONFIG_DIR=~/.claude-cc3 claude <transcript seed, 1247 chars>
  in ~/src/api
```

Seeded forks still write their handoff file in a dry run, so you can read
exactly what the target agent would have been given, in
`~/.cache/agentbridge/handoffs/`.

## Hooks

**Arrive hooks.** After `agb push` lands a session on another machine, it can
run a command there, typically to open the session in a terminal multiplexer
your phone can see. Configure one per host under `arrive`; the
[push and pull guide](../push-pull.md) has the placeholders and an example for
Herdr.

**Herdr.** `agb herdr install` adds a session hook to each Claude and Codex
account so Herdr can restore conversations in the right account after a
restart. See [Switching agents in Herdr](../herdr.md).

## Environment variables

| Variable | Effect |
| --- | --- |
| `AGB_CONFIG_PATH` | Read config from here instead of `~/.config/agentbridge/config.json` |
| `AGB_CACHE_PATH` | Keep the parse cache here; limits, layout and handoffs sit beside it |
| `AGB_DRY_RUN` | Print launches instead of running them |
| `AGB_MACHINE_NAME` | This machine's name for `moveHosts`, instead of the host name |
| `CLAUDE_PROJECTS_PATH` | Read Claude transcripts from here |
| `CODEX_HOME`, `CODEX_SESSIONS_PATH` | The default Codex home or sessions directory |
| `OPENCODE_DB_PATH` | The default OpenCode database |

Every path `agb` reads, and why it starts fast, is in
[configuration and internals](../reference.md).

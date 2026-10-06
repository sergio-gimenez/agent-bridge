# Using multiple Codex accounts

`agb` can index and launch sessions from multiple Codex accounts. The mechanism
is the same one described in
[Using multiple Claude Code accounts](multiple-claude-accounts.md), namely an
isolated home directory per account, so this page covers only what differs for
Codex.

## Prerequisites

- Two or more accounts you can sign into with `codex login`
- Codex installed and available as `codex`
- `agb` installed from this repository

## How isolation works

Codex keeps credentials, config and session history under one home directory,
named by `CODEX_HOME` and defaulting to `~/.codex`:

- Session rollouts: `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`
- Archived rollouts: `~/.codex/archived_sessions/...`
- Credentials: `~/.codex/auth.json`
- Config: `~/.codex/config.toml`

Pointing `CODEX_HOME` somewhere else gives you a completely separate account.
Do not set `CODEX_HOME=~/.codex` for the default account; leave it unset instead,
so the default home keeps working when the variable is not exported.

## 1. Create a wrapper per account

```bash
cx1() { command codex "$@"; }
cx2() { CODEX_HOME="$HOME/.codex-cx2" command codex "$@"; }
```

PowerShell and the persistence notes in the Claude document apply unchanged;
substitute `CODEX_HOME` for `CLAUDE_CONFIG_DIR`.

## 2. Authenticate each account

```bash
cx1 login
cx2 login
```

Run each login separately and pick the corresponding account in the browser.
Verify with `cx1 login status` and `cx2 login status`.

## 3. Tell `agb` about the accounts

In `~/.config/agentbridge/config.json`:

```json
{
  "codexAccounts": [
    {
      "name": "cx1"
    },
    {
      "name": "cx2",
      "codexHome": "~/.codex-cx2"
    }
  ],
  "defaultCodexAccount": "cx1"
}
```

The account without `codexHome` is Codex's default home. Account names must be
unique across *all* tools, since they name one shared list of targets, and they
become picker badges such as `[CX1]` and `[CX2]`.

## 4. Use the picker

Controls are the same as for Claude accounts. `Ctrl+T` and `Shift+Tab` cycle one
target list covering OpenCode, every Claude account and every Codex account, and
`Enter` follows whatever route the badge shows.

```bash
agb --codex-account cx2    # or --target cx2
```

## Resume and fork behavior

Codex session ids belong to the home that created them, so `agb` uses two paths:

- Same account: `codex resume <id>` with the owning account's environment, or
  `codex fork <id>` for `Ctrl+F`, which branches natively.
- Different account or tool: build a continuation prompt from the rollout and
  start a fresh session in the target with that context.

## Differences worth knowing

- **Titles.** Codex does not record a title in the rollout, so `agb` titles a
  Codex row with its opening prompt. Claude and OpenCode rows use the title
  their tool stored.
- **Archiving.** Codex archives a session by moving its rollout to
  `archived_sessions/`. `agb` reads only `sessions/`, so archived Codex sessions
  drop out of the list.
- **Setup context.** Codex opens a session by feeding itself the environment
  block and any `AGENTS.md` as user turns. `agb` filters those out, so they
  never become a title or match a search for something you typed.

## Troubleshooting

### No CX2 sessions appear

An authenticated account has no rollouts until it starts a conversation. Run
`cx2` once, or fork a session into CX2 with `Enter` on a `[… →CX2]` row.

### The wrong account opens

Check both with `cx1 login status` and `cx2 login status`, and confirm the
`codexHome` in the config matches the value used by the `cx2` wrapper.

### Protect credentials

Do not copy or commit `auth.json` from any Codex home. The `agb` config holds
directory locations only.

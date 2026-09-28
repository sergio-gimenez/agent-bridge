# Using multiple Claude Code accounts

`ocs` can index and launch sessions from multiple Claude Code subscriptions.
Each account keeps separate authentication, settings, and session storage while
all accounts can work in the same project directories.

## Prerequisites

- Two or more Claude subscriptions, each using a different Claude account
- Claude Code installed and available as `claude`
- `ocs` installed from this repository

Install `ocs` locally when needed:

```bash
make install
```

## How isolation works

Claude Code normally stores its first account in its default home:

- Session data: `~/.claude/projects`
- Main metadata: `~/.claude.json`

Additional accounts use `CLAUDE_CONFIG_DIR`. For example,
`CLAUDE_CONFIG_DIR=~/.claude-cc2` places the second account's credentials,
settings, and sessions below `~/.claude-cc2`.

Do not set `CLAUDE_CONFIG_DIR=~/.claude` for the default account. The default
layout is special because `~/.claude.json` sits outside `~/.claude`. Launch the
default account without `CLAUDE_CONFIG_DIR` instead.

## 1. Create account commands

### Zsh or Bash

Add these functions to `~/.zshrc` or `~/.bashrc`:

```bash
cc1() {
  command claude "$@"
}

cc2() {
  CLAUDE_CONFIG_DIR="$HOME/.claude-cc2" command claude "$@"
}
```

Reload the shell:

```bash
source ~/.zshrc
```

Use `source ~/.bashrc` when using Bash.

### PowerShell

Add these functions to the PowerShell profile shown by `$PROFILE`:

```powershell
function cc1 {
    claude @args
}

function cc2 {
    $previous = $env:CLAUDE_CONFIG_DIR
    try {
        $env:CLAUDE_CONFIG_DIR = Join-Path $HOME ".claude-cc2"
        claude @args
    }
    finally {
        if ($null -eq $previous) {
            Remove-Item Env:\CLAUDE_CONFIG_DIR -ErrorAction SilentlyContinue
        }
        else {
            $env:CLAUDE_CONFIG_DIR = $previous
        }
    }
}
```

Reload the profile:

```powershell
. $PROFILE
```

## 2. Authenticate both subscriptions

Run each login separately and choose the corresponding Claude account in the
browser:

```bash
cc1 auth login
cc2 auth login
```

Verify that both homes are authenticated:

```bash
cc1 auth status
cc2 auth status
```

Each command should report `"loggedIn": true` and the expected account. The two
config homes do not share credentials.

## 3. Configure `ocs`

Create `~/.config/ocs/config.json`:

```json
{
  "skipPermissions": false,
  "claudeAccounts": [
    {
      "name": "cc1"
    },
    {
      "name": "cc2",
      "configDir": "~/.claude-cc2"
    }
  ],
  "defaultClaudeAccount": "cc1"
}
```

The account without `configDir` is Claude Code's default home. Account names
must be unique. They become picker badges such as `[CC1]` and `[CC2]`.

`defaultClaudeAccount` is the account Claude routes land on when a route reaches
Claude without naming one, and it leads the Claude group when the target is
cycled. It does not change which account owns existing sessions.

## 4. Use the picker

Start normally:

```bash
ocs
```

Picker controls:

| Key | Action |
| --- | --- |
| `Enter` | Follow the displayed route |
| `Ctrl+F` | Fork the displayed route into a new session |
| `Ctrl+T` | Cycle the target forwards, across every tool and account |
| `Shift+Tab` | Cycle the target backwards |
| `Tab` | Open in the next tool as a transcript-seeded session |

Badges preview the selected route. Until the target is cycled it follows the
selection, so every row shows a single label and `Enter` resumes it in its own
account. With CC2 targeted, a CC1 session is shown as `[CC1→CC2]`; a session
already owned by CC2 remains `[CC2]`. Cycling back to CC1 reverses cross-account
routes to `[CC2→CC1]`.

Set the initial target from the command line:

```bash
ocs --claude-account cc2    # or --target cc2
```

### Continue an old CC1 session with CC2

1. Run `ocs --claude-account cc2`.
2. Select the old `[CC1]` session.
3. Confirm the picker says `Target: CC2` and the badge reads `[CC1→CC2]`.
4. Press `Enter`.

`ocs` reads the CC1 transcript and starts a fresh CC2 session with that context.
The original CC1 session remains unchanged. Once Claude Code persists the new
session, it appears in `ocs` with a `[CC2]` badge.

## Resume and fork behavior

Claude session IDs belong to the config home that created them. `ocs` therefore
uses two different paths:

- Same account: launch `claude --resume` with the owning account's environment.
- Different account: build a fresh continuation prompt from the transcript and
  launch Claude with the target account's environment.

The same two paths apply to a route that crosses tools, with the target tool's
own resume command and environment.

A cross-account fork carries conversation text, not hidden model state or the
source account's credentials. Both sessions still use the same working
directory, so they see the same project files and Git worktree.

## Add more accounts

Create another wrapper and config entry for each account:

```bash
cc3() {
  CLAUDE_CONFIG_DIR="$HOME/.claude-cc3" command claude "$@"
}
```

```json
{
  "name": "cc3",
  "configDir": "~/.claude-cc3"
}
```

Authenticate it with `cc3 auth login`. `Ctrl+T` cycles through all configured
accounts.

## Troubleshooting

### No CC2 sessions appear

An authenticated account has no session files until it starts a conversation.
Run `cc2` once or fork a session into CC2 with `Enter` on a `[CC1→CC2]` row.

### The wrong subscription opens

Check both identities with `cc1 auth status` and `cc2 auth status`. Confirm the
second `configDir` matches the value used by the `cc2` wrapper.

### Old sessions disappear

Keep the first account entry without `configDir`. This lets `ocs` continue
scanning `~/.claude/projects`, where existing default-account sessions live.

### A session resumes in the wrong account

Do not move session JSONL files between config homes. Keep account definitions
stable after sessions exist; `ocs` records ownership from the directory it
scanned.

### Protect credentials

Do not copy or commit `.credentials.json` from any Claude config directory. The
`ocs` config contains directory locations only and does not need tokens or
passwords.

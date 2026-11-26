---
title: Install
description: Install agb, install the coding agents you use, and run it for the first time.
---

# Install

`agb` is a single static binary for Linux and macOS, on amd64 or arm64. It
doesn't need root, a runtime or a daemon, and it reads the session stores the
agents already write, so there's nothing to import.

## 1. Install agb

```bash
curl -fsSL https://agentbridge.sergiogimenez.com/install.sh | sh
```

The script puts `agb` in `~/.local/bin` and checks the download against the
release's checksums before it does. [Read it first](https://github.com/sergio-gimenez/agent-bridge/blob/main/scripts/install.sh)
if you'd rather; it's short.

To pin a release, pass the version through:

```bash
curl -fsSL https://agentbridge.sergiogimenez.com/install.sh | AGB_VERSION=v2.1.0 sh
```

Rerun the same line to update, and `agb version` tells you what you've got.
Archives for every release are on the
[releases page](https://github.com/sergio-gimenez/agent-bridge/releases).

??? note "Build from source instead"

    You need Go 1.23 or newer.

    ```bash
    git clone https://github.com/sergio-gimenez/agent-bridge.git
    cd agent-bridge && make install
    ```

??? note "Coming from `opencode-sessions` or `ocs`?"

    Configuration and cache moved to `~/.config/agentbridge` and
    `~/.cache/agentbridge`, and environment overrides use `AGB_*`. Move your old
    config and cache there before removing the previous install. Both
    installers delete the obsolete command aliases.

## 2. Install at least one agent

`agb` lists and launches agents; it doesn't replace them. Install whichever you
use with its own installer, sign in once, and have at least one conversation
so there's something to list.

| Agent | Install (Linux and macOS) | Sign in | Guide |
| --- | --- | --- | --- |
| <span class="agb-label agb-label--cc">CC</span> Claude Code | `curl -fsSL https://claude.ai/install.sh \| bash` | `claude auth login` | [Claude Code](../agents/claude-code.md) |
| <span class="agb-label agb-label--cx">CX</span> Codex | `curl -fsSL https://chatgpt.com/codex/install.sh \| sh` | `codex login` | [Codex](../agents/codex.md) |
| <span class="agb-label agb-label--oc">OC</span> OpenCode | `curl -fsSL https://opencode.ai/v2/install \| bash` | `opencode auth login` | [OpenCode](../agents/opencode.md) |
| <span class="agb-label agb-label--ag">AG</span> Antigravity | `curl -fsSL https://antigravity.google/cli/install.sh \| bash` | run `agy` | [Antigravity](../agents/antigravity.md) |

Those are the vendors' own commands, current as of November 2026. Each one
also ships through Homebrew or npm; the official pages
([Claude Code](https://code.claude.com/docs/en/setup),
[Codex](https://learn.chatgpt.com/docs/codex/cli),
[OpenCode](https://opencode.ai/docs),
[Antigravity](https://antigravity.google/docs/getting-started?tab=cli))
have the alternatives and change more often than this page does.

You don't have to tell `agb` anything for a single account per tool. It finds
each tool's default store by itself, and a tool that isn't installed is simply
skipped.

## 3. First run

```bash
agb
```

The picker opens with every session from every store, newest first. Start
typing to filter, `Enter` to resume the selected one, `Esc` to leave.
[Using the picker](picker.md) covers the rest of the keys.

Two quick ways to check what `agb` found without the full-screen picker:

```bash
agb --print            # the 25 most recent sessions, as text
agb limits             # each account's remaining quota and the suggested one
```

Nothing listed for a tool you know you've used? Run `agb --print --rescan` to
skip the cache, and see [where the data comes from](../reference.md#where-the-data-comes-from)
for the exact paths it reads.

## Try it without your own history

```bash
git clone https://github.com/sergio-gimenez/agent-bridge.git
cd agent-bridge && make demo
```

`make demo` builds a made-up history (two Claude accounts, two Codex accounts,
an OpenCode store, four invented projects and fake limits) and runs the real
picker against it with launching stubbed out. A second pretend machine called
`desk` is there for trying push and pull. None of your files are read.

## Next

- Several accounts for one tool: [Claude Code](../agents/claude-code.md#3-configure-agb),
  [Codex](../agents/codex.md#3-tell-agb-about-the-accounts)
- [Using the picker](picker.md)
- [Forks and handoffs](../forking.md), which explains what happens when you
  open a session in a different tool

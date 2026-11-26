---
title: Agents
description: What agb can do with Claude Code, Codex, OpenCode and the Antigravity CLI, side by side.
---

# Agents

`agb` reads four agents' session stores and launches the agents themselves for
everything else. They don't all expose the same things, so not every feature
works everywhere. This is the honest table:

<div class="agb-matrix" markdown>

| | <span class="agb-label agb-label--cc">CC</span> Claude Code | <span class="agb-label agb-label--cx">CX</span> Codex | <span class="agb-label agb-label--oc">OC</span> OpenCode | <span class="agb-label agb-label--ag">AG</span> Antigravity |
| --- | --- | --- | --- | --- |
| Listed and searchable | yes | yes | yes | yes |
| Resume in place | `claude --resume` | `codex resume` | `opencode --session` | `agy --conversation` |
| Native fork (`Ctrl+F`) | yes | yes | OpenCode 1 only | no, seeded instead |
| Fork in from another tool | yes | yes | yes | yes |
| Several accounts | `CLAUDE_CONFIG_DIR` | `CODEX_HOME` | wrapper stores | one account |
| Limit bars and suggestions | yes | yes | `unknown` | `unknown` |
| Push and pull between machines | yes | yes | no | no |
| Restore after a Herdr restart | yes | yes | no | no |
| `agb sync` (skills, MCPs) | yes | yes | yes | not yet |
| Secret conversations | no | no | yes | no |

</div>

A "seeded" fork means `agb` starts a fresh session and hands it the old
transcript as context, plus a file with the full history. It's what every
cross-tool move does anyway; see [forks and handoffs](../forking.md).

Most of the "no" cells are missing interfaces on the agent's side rather than
work nobody got round to. OpenCode is provider-agnostic, so it has no single
subscription to measure. `agy` reports its quota only through `/usage` inside
its own TUI. Push and pull copy the agent's own session so the other machine
resumes the same id, which is easy when a session is one file, as it is for
Claude Code and Codex. OpenCode keeps sessions in a shared SQLite database, and
`agy` spreads one conversation over a step log, a per-conversation database and
a shared index.

## Pick a guide

<div class="grid cards" markdown>

-   <span class="agb-label agb-label--cc">CC</span> [**Claude Code**](claude-code.md)

    One account works out of the box. This guide adds a second (and a third),
    each with its own `CLAUDE_CONFIG_DIR`, and covers moving a conversation
    between them.

-   <span class="agb-label agb-label--cx">CX</span> [**Codex**](codex.md)

    Same idea with `CODEX_HOME`, plus what's different about Codex rollouts
    (no titles, archiving by moving files).

-   <span class="agb-label agb-label--oc">OC</span> [**OpenCode**](opencode.md)

    The default store, wrapper commands with stores of their own, and the
    differences between OpenCode 1 and 2.

-   <span class="agb-label agb-label--ag">AG</span> [**Antigravity**](antigravity.md)

    Google's `agy` CLI and its Gemini 4 models: install, sign in, what `agb`
    reads from it and what it can't yet.

</div>

## Account names

Every account across every tool shares one list of targets, so names have to be
unique across tools: `cc1`, `cc2`, `cx1`, `oc`, `ag`. They become the labels on
the rows (`CC2`) and what `--target` takes:

```bash
agb --target cx2
agb new --target ag --dir ~/src/api
```

All of it lives in `~/.config/agentbridge/config.json`. The full set of keys is
in [configuration and internals](../reference.md).

Want a fifth agent? [Adding an agent](../extend/adding-an-agent.md) walks
through what a new one needs, using Antigravity as the worked example.

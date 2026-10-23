# Memory and diagrams across tools

A session that moves between tools works best when every tool can reach the same
memory and draw the same diagrams. None of this is part of `agb`; these are
third-party pieces that `make install` suggests when they are missing.

## Sharing memory between tools

A fork carries the conversation, not what the agent has saved to memory.
Claude Code keeps that in `~/.claude/projects/<project>/memory/`, and two
third-party plugins let the other tools use the same files.

- **OpenCode:** [`opencode-claude-memory`](https://github.com/kuitos/opencode-claude-memory)
  reads and writes the memory directory and respects `CLAUDE_CONFIG_DIR`. Add
  `"plugin": ["opencode-claude-memory"]` to `opencode.json`.
- **Codex:** [`codex-claude-memory-plugin`](https://github.com/gaboe/codex-claude-memory-plugin)
  loads the project's `MEMORY.md` when a session starts and writes to it only when you ask
  Codex to remember something. It is young and always reads `~/.claude`, so
  other Claude accounts' memory isn't picked up.

  ```bash
  codex plugin marketplace add gaboe/codex-claude-memory-plugin
  codex plugin add codex-claude-memory-plugin@codex-claude-memory
  ```

To share memory between Claude Code accounts, see
[sharing Claude Code memory between accounts](setup.md#share-claude-code-memory-between-accounts).

## Rendering diagrams in every tool

A forked session often carries a diagram with it. [`claude-mermaid`](https://github.com/veelenga/claude-mermaid)
is a single stdio MCP server that renders Mermaid and live-reloads a browser
preview, so all three tools can point at the same binary and a diagram looks the
same wherever the session lands.

```bash
npm install -g claude-mermaid
```

- **Claude Code:** `claude mcp add --scope user mermaid claude-mermaid`, or
  install the plugin with `/plugin marketplace add veelenga/claude-mermaid`.
- **OpenCode:** add the server to `opencode.json`.

  ```json
  "mcp": {
    "mermaid": { "type": "local", "command": ["claude-mermaid"], "enabled": true }
  }
  ```

- **Codex:** add the server to `~/.codex/config.toml`.

  ```toml
  [mcp_servers.mermaid]
  command = "claude-mermaid"
  ```

The repository also ships a `mermaid-diagrams` skill. It is written for Claude
Code, but the file drops into `~/.codex/skills/` and
`~/.config/opencode/skill/` as-is once you remove its `allowed-tools:` line,
which names Claude Code's tool ids. The preview opens a local browser tab, so it
is only useful for sessions running on your own machine.

For diagrams the agent should keep editing, [`mcp-excalidraw-server`](https://github.com/yctimlin/mcp_excalidraw)
gives every tool the same live Excalidraw canvas. The agent can draw on it, take
a screenshot, fix what it sees and export a `.excalidraw` file. It is again one
stdio binary, and it starts the canvas on `127.0.0.1:3000` by itself.

```bash
npm install -g mcp-excalidraw-server
```

- **Claude Code:** `claude mcp add --scope user excalidraw -- mcp-excalidraw-server`
- **OpenCode:** `"excalidraw": { "type": "local", "command": ["mcp-excalidraw-server"], "enabled": true }`
  under `"mcp"` in `opencode.json`.
- **Codex:** `codex mcp add excalidraw -- mcp-excalidraw-server`

Its skill has no tool ids in it, so one command installs it for each tool:

```bash
for root in ~/.claude/skills ~/.codex/skills ~/.config/opencode/skill; do
  mcp-excalidraw-server install-skill --dir "$root"
done
```

Both servers are also a good first entry for a [shared setup profile](setup.md),
which writes the same MCP definition into every tool and account for you.

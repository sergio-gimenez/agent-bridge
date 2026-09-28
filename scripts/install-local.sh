#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="${HOME}/.local/bin"
TARGET="${BIN_DIR}/ocs"
LEGACY_TARGET="${BIN_DIR}/ocsessions"

mkdir -p "${BIN_DIR}"
cd "${ROOT_DIR}"

npm run build >/dev/null

cat >"${TARGET}" <<EOF
#!/usr/bin/env bash
exec node "${ROOT_DIR}/dist/cli.js" "\$@"
EOF

chmod +x "${TARGET}"
rm -f "${LEGACY_TARGET}"

printf 'Installed ocs at %s\n' "${TARGET}"

# ocs carries a conversation between tools, but not what the agent remembers.
# Point at the plugins that share Claude Code's memory files with the other two,
# only for tools that are installed and don't have one set up yet.
has_tool() {
  command -v "$1" >/dev/null 2>&1 || [ -d "$2" ]
}

OPENCODE_CONFIG_DIR="${XDG_CONFIG_HOME:-${HOME}/.config}/opencode"
CODEX_DIR="${CODEX_HOME:-${HOME}/.codex}"
tips=()

if has_tool opencode "${OPENCODE_CONFIG_DIR}" \
  && ! grep -qs 'opencode-claude-memory' "${OPENCODE_CONFIG_DIR}"/opencode.json*; then
  tips+=(
    ""
    "  OpenCode: add the plugin to ${OPENCODE_CONFIG_DIR}/opencode.json"
    '    "plugin": ["opencode-claude-memory"]'
    "    https://github.com/kuitos/opencode-claude-memory"
  )
fi

if has_tool codex "${CODEX_DIR}" \
  && ! compgen -G "${CODEX_DIR}/plugins/cache/*/codex-claude-memory-plugin" >/dev/null; then
  tips+=(
    ""
    "  Codex: loads MEMORY.md at session start (reads ~/.claude only)"
    "    codex plugin marketplace add gaboe/codex-claude-memory-plugin"
    "    codex plugin add codex-claude-memory-plugin@codex-claude-memory"
    "    https://github.com/gaboe/codex-claude-memory-plugin"
  )
fi

if [ "${#tips[@]}" -gt 0 ]; then
  printf '\nTip: share Claude Code memory with your other tools, so a session you\n'
  printf 'move with ocs keeps what the agent has learned about the project.\n'
  printf '%s\n' "${tips[@]}"
fi

# Same idea for tooling: a forked session that asked for a diagram should be
# able to render it wherever it lands. claude-mermaid is one stdio MCP server,
# so all three tools can point at the same binary.
CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-${HOME}/.claude}"
MERMAID_BIN="$(command -v claude-mermaid || true)"
diagram_tips=()

if [ -z "${MERMAID_BIN}" ]; then
  diagram_tips+=(
    ""
    "  Install the server once:"
    "    npm install -g claude-mermaid"
  )
  MERMAID_BIN="claude-mermaid"
fi

if has_tool claude "${CLAUDE_DIR}" \
  && ! grep -qs 'claude-mermaid' "${HOME}/.claude.json"; then
  diagram_tips+=(
    ""
    "  Claude Code:"
    "    claude mcp add --scope user mermaid ${MERMAID_BIN}"
  )
fi

if has_tool opencode "${OPENCODE_CONFIG_DIR}" \
  && ! grep -qs 'claude-mermaid' "${OPENCODE_CONFIG_DIR}"/opencode.json*; then
  diagram_tips+=(
    ""
    "  OpenCode: add the server to ${OPENCODE_CONFIG_DIR}/opencode.json"
    "    \"mcp\": { \"mermaid\": { \"type\": \"local\","
    "                 \"command\": [\"${MERMAID_BIN}\"], \"enabled\": true } }"
  )
fi

if has_tool codex "${CODEX_DIR}" \
  && ! grep -qs 'claude-mermaid' "${CODEX_DIR}/config.toml"; then
  diagram_tips+=(
    ""
    "  Codex: add the server to ${CODEX_DIR}/config.toml"
    "    [mcp_servers.mermaid]"
    "    command = \"${MERMAID_BIN}\""
  )
fi

if [ "${#diagram_tips[@]}" -gt 0 ]; then
  printf '\nTip: render Mermaid diagrams in every tool, so a session you move with\n'
  printf 'ocs can still draw one. https://github.com/veelenga/claude-mermaid\n'
  printf '%s\n' "${diagram_tips[@]}"
fi

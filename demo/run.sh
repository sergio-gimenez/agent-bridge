#!/usr/bin/env bash
# Runs AgentBridge against the synthetic demo fixture instead of your real sessions.
#
# HOME points at the fixture home, so agb finds its config, the OpenCode
# database and every Claude and Codex account at their normal default paths.
# Opening a session is stubbed out (AGB_DRY_RUN), so nothing is ever launched.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"

cd "$root"
go build -o bin/agb ./cmd/agb
go run ./demo/fixture >/dev/null

export HOME="$here/.fixture/home"
export AGB_DRY_RUN=1
unset CLAUDE_CONFIG_DIR CLAUDE_PROJECTS_PATH CODEX_HOME CODEX_SESSIONS_PATH \
      OPENCODE_DB_PATH AGB_CONFIG_PATH AGB_CACHE_PATH OCS_CONFIG_PATH \
      OCS_CACHE_PATH OCS_DRY_RUN XDG_CACHE_HOME

# Ctrl+O and Ctrl+R reach "desk", a second fixture home standing in for another
# machine (see demo/README.md).
export AGB_DEMO_REMOTES="desk=$here/.fixture/desk"
export AGB_MACHINE_NAME=laptop
export GIT_CEILING_DIRECTORIES="$here/.fixture"
export PATH="$HOME/.local/bin:$PATH"

exec "$root/bin/agb" "$@"

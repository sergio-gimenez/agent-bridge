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

exec "$root/bin/agb" "$@"

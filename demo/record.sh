#!/usr/bin/env bash
# Records the demo against the synthetic fixture and writes docs/demo.cast.
#
# Regenerating the cast needs only asciinema. Turning it into the GIF that the
# README embeds also needs agg; see demo/README.md.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
cast="$root/docs/demo.cast"

command -v asciinema >/dev/null || { echo "asciinema not found" >&2; exit 1; }

cd "$root"
go build -o bin/agb ./cmd/agb
go run ./demo/fixture
mkdir -p "$root/docs"

export HOME="$here/.fixture/home"
export AGB_DRY_RUN=1
# Nothing from the recorder's own environment should reach the recording.
unset CLAUDE_CONFIG_DIR CLAUDE_PROJECTS_PATH CODEX_HOME CODEX_SESSIONS_PATH \
      OPENCODE_DB_PATH AGB_CONFIG_PATH AGB_CACHE_PATH OCS_CONFIG_PATH \
      OCS_CACHE_PATH OCS_DRY_RUN XDG_CACHE_HOME

bash "$here/keys.sh" | asciinema rec \
  --overwrite --quiet \
  --cols 120 --rows 32 \
  --title "AgentBridge - sessions and setup across coding agents" \
  --command "$root/bin/agb" \
  "$cast"

echo "Wrote ${cast#"$root"/}"

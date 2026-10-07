#!/usr/bin/env bash
# Records a demo against the synthetic fixture and writes docs/<name>.cast.
#
#   demo/record.sh          the picker tour       (keys.sh      -> docs/demo.cast)
#   demo/record.sh push     push to "desk"        (keys-push.sh -> docs/push.cast)
#   demo/record.sh pull     browse and pull       (keys-pull.sh -> docs/pull.cast)
#
# Regenerating a cast needs only asciinema. Turning it into the GIF that the
# docs embed also needs agg; see demo/README.md.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
scenario="${1:-demo}"
case "$scenario" in
  demo) keys="$here/keys.sh"; title="AgentBridge - sessions and setup across coding agents" ;;
  push) keys="$here/keys-push.sh"; title="AgentBridge - push a session to another machine" ;;
  pull) keys="$here/keys-pull.sh"; title="AgentBridge - browse another machine and pull a session" ;;
  *) echo "unknown demo: $scenario (demo, push or pull)" >&2; exit 1 ;;
esac
cast="$root/docs/$scenario.cast"

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

# "desk" is a second fixture home standing in for another machine, the machine
# is "laptop" rather than whatever records this, and git stops at the fixture
# instead of finding this repository around it.
export AGB_DEMO_REMOTES="desk=$here/.fixture/desk"
export AGB_MACHINE_NAME=laptop
export GIT_CEILING_DIRECTORIES="$here/.fixture"
export PATH="$HOME/.local/bin:$PATH"

# The pull demo meets a session still open on desk: a process started to
# resume it, which is what agb looks for.
if [ "$scenario" = pull ]; then
  open_id=$(basename "$(ls "$here"/.fixture/desk/.claude-cc2/projects/*/*.jsonl | head -1)" .jsonl)
  bash -c 'sleep 600; :' claude --resume "$open_id" </dev/null >/dev/null 2>&1 &
  open_pid=$!
  trap 'pkill -P $open_pid 2>/dev/null; kill $open_pid 2>/dev/null || true' EXIT
fi

bash "$keys" | asciinema rec \
  --overwrite --quiet \
  --cols 120 --rows 32 \
  --title "$title" \
  --command "$root/bin/agb" \
  "$cast"

echo "Wrote ${cast#"$root"/}"

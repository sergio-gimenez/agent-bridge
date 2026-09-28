#!/usr/bin/env bash
# Types the demo script into the picker.
#
# This writes raw terminal input on stdout with pauses between keystrokes. Pipe
# it into `asciinema rec`, which forwards its stdin to the recorded process's
# pty, so the picker sees a real interactive session and the pauses land in the
# recording as human-paced typing.
set -euo pipefail

DOWN=$'\e[B'
UP=$'\e[A'
ENTER=$'\r'
CTRL_T=$'\x14'

# pause MS: wait before the next step.
pause() { sleep "$(printf '%d.%03d' $(($1 / 1000)) $(($1 % 1000)))"; }

# key STRING: send one keypress.
key() { printf '%s' "$1"; }

# type TEXT: send each character with a typing delay between them.
type_text() {
  local text=$1 i
  for ((i = 0; i < ${#text}; i++)); do
    ((i > 0)) && pause 120
    key "${text:i:1}"
  done
}

# Let the merged list settle: OpenCode, two Claude accounts and two Codex
# accounts in one list, newest first.
pause 2000

# Move down a few entries so the preview pane visibly follows the selection.
pause 900; key "$DOWN"
pause 700; key "$DOWN"
pause 900; key "$DOWN"

# Search by project. Every nebula-api session surfaces at once, whichever tool
# it happens to live in.
pause 1300; type_text "nebula"
pause 1900

# Second row is the Codex session. Its badge stays [CX1] and the preview fills
# with its prompts, because the target follows the selection.
pause 900; key "$DOWN"
pause 2200

# Back up to the Claude session at the top.
pause 1100; key "$UP"
pause 1200

# Cycle the target twice: CC1 -> CC2 -> CX1. The badge turns into a route,
# [CC1->CX1], so Enter will carry this Claude session into Codex instead of
# resuming it in Claude.
pause 1000; key "$CTRL_T"
pause 1300; key "$CTRL_T"
pause 2600

# Follow the displayed route.
pause 900; key "$ENTER"
pause 2200

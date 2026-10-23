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
# accounts in one list, newest first, and the header suggesting the account
# with the most headroom.
pause 2600

# Move down a few entries so the card visibly follows the selection.
pause 800; key "$DOWN"
pause 900; key "$DOWN"
pause 1400

# Search what you asked, not only titles. "retries" was in a prompt to Claude
# and in one to Codex, so both sessions surface and the card lights it up.
pause 900; type_text "retries"
pause 2200

# The Codex session, then back up to the Claude one.
pause 700; key "$DOWN"
pause 2000
pause 700; key "$UP"
pause 1600

# Ctrl+T asks where to open it, with every account's limits beside it. CC1 is
# nearly out of its 5h window; CX1 is the suggestion.
pause 800; key "$CTRL_T"
pause 2600
pause 600; key "$DOWN"
pause 700; key "$DOWN"
pause 2000

# Open it in CX1. Across tools that is a transcript-seeded fork, printed rather
# than launched because the demo runs with AGB_DRY_RUN.
pause 800; key "$ENTER"
pause 2600

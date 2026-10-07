#!/usr/bin/env bash
# Types the push demo: hand a session to "desk" from the picker, then browse
# desk to see it arrived. See keys.sh for how the keystrokes reach the picker.
set -euo pipefail

ENTER=$'\r'
ESC=$'\e'
CTRL_O=$'\x0f'
CTRL_R=$'\x12'

pause() { sleep "$(printf '%d.%03d' $(($1 / 1000)) $(($1 % 1000)))"; }
key() { printf '%s' "$1"; }
type_text() {
  local text=$1 i
  for ((i = 0; i < ${#text}; i++)); do
    ((i > 0)) && pause 120
    key "${text:i:1}"
  done
}

pause 1800

# Find the session to hand over.
type_text "rayon"
pause 1500

# Ctrl+O: the card turns into the push panel. It finds the project on desk,
# checks both sides, and says what happens once the session is there.
key "$CTRL_O"
pause 4500

# Push. The arrive hook opens it in a herdr tab on desk.
key "$ENTER"
pause 3200

# Back to the list, then Ctrl+R to browse desk: the session is there now.
key "$ENTER"
pause 900
for _ in 1 2 3 4 5; do key $'\x7f'; pause 60; done
pause 700
key "$CTRL_R"
pause 3800

# Esc goes home, Esc again quits.
key "$ESC"
pause 1500
key "$ESC"
pause 400

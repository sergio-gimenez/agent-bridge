#!/usr/bin/env bash
# Types the pull demo: browse "desk", meet a session still open there, then
# pull another one and resume it here. See keys.sh for how the keystrokes reach
# the picker; record.sh starts the "open on desk" process this needs.
set -euo pipefail

DOWN=$'\e[B'
ENTER=$'\r'
CTRL_R=$'\x12'

pause() { sleep "$(printf '%d.%03d' $(($1 / 1000)) $(($1 % 1000)))"; }
key() { printf '%s' "$1"; }

pause 1800

# Ctrl+R: the list becomes desk's sessions. The header says which machine.
key "$CTRL_R"
pause 3200

# The top one is still running on desk, so it cannot come over yet.
key "$ENTER"
pause 3500

# The next one is free: Enter pulls it and resumes it here.
key "$DOWN"
pause 2200
key "$ENTER"
pause 3500

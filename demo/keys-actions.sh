#!/usr/bin/env bash
# Show real SGR mouse reports opening the conversation menu, then create a
# blank Codex conversation in a chosen project. Opening remains a dry run.
set -euo pipefail

pause() { sleep "$(printf '%d.%03d' $(($1 / 1000)) $(($1 % 1000)))"; }
key() { printf '%s' "$1"; }
mouse() { printf '\033[<%d;%d;%dM\033[<%d;%d;%dm' "$1" "$2" "$3" "$1" "$2" "$3"; }
type_text() {
  local text=$1 i
  for ((i = 0; i < ${#text}; i++)); do
    ((i > 0)) && pause 90
    key "${text:i:1}"
  done
}

pause 1600
type_text "rayon"
pause 1400

# Right-click the filtered conversation, then scroll through its actions.
mouse 2 8 3
pause 2000
mouse 65 8 7
pause 900
mouse 65 8 7
pause 1600

# Select the penultimate item: New conversation in this project. Keyboard
# navigation avoids relying on the number of configured accounts in the menu.
key $'\e[F'
key $'\e[A'
pause 1500
key $'\r'
pause 1800

# Click agent/account in the footer: CC2 -> CX1. Toggle YOLO by clicking its
# footer action, then turn it back off for the blank conversation we launch.
mouse 0 30 32
pause 1700
mouse 0 48 32
pause 1400
mouse 0 48 32
pause 1000

# Choose another existing fixture project and click Start.
key $'\x15'
type_text "~/code/nebula-api"
pause 1800
mouse 0 15 32
pause 1600

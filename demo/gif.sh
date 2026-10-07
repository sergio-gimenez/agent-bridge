#!/usr/bin/env bash
# Renders docs/<name>.cast (demo, push or pull; default demo) into the
# docs/<name>.gif that the docs embed.
#
# Uses agg (https://github.com/asciinema/agg). Prefers a local binary and falls
# back to the official container image, so this works with either installed.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
name="${1:-demo}"

[ -f "$root/docs/$name.cast" ] || { echo "No docs/$name.cast — run demo/record.sh $name first." >&2; exit 1; }

args=(--theme github-dark --font-size 14 --line-height 1.35 --fps-cap 20 --idle-time-limit 2)

if command -v agg >/dev/null; then
  agg "${args[@]}" "$root/docs/$name.cast" "$root/docs/$name.gif"
elif command -v docker >/dev/null; then
  # --user keeps the rendered GIF owned by the caller rather than root.
  docker run --rm --user "$(id -u):$(id -g)" -v "$root/docs:/data" \
    ghcr.io/asciinema/agg "${args[@]}" "$name.cast" "$name.gif"
else
  echo "Need either agg or docker on PATH." >&2
  exit 1
fi

echo "Wrote docs/$name.gif ($(du -h "$root/docs/$name.gif" | cut -f1))"

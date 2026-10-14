#!/usr/bin/env bash

# Stages README.md and docs/ into one Zensical source tree and builds the site.
#
# The Markdown stays written for GitHub, where README.md sits at the root and
# links into docs/. On the site README.md becomes index.md beside the other
# pages, so its links are rewritten here instead of in the sources.
#
# Usage: build-docs.sh [build|serve]

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
STAGE="${ROOT_DIR}/.site-src"
REPO_BLOB="https://github.com/sergio-gimenez/agent-bridge/blob/main"
ZENSICAL="${ZENSICAL:-zensical}"

cd "${ROOT_DIR}"

stage() {
  mkdir -p "${STAGE}"
  cp -R docs/. "${STAGE}/"

  # README links point into docs/ or at repo files the site does not carry.
  sed -E \
    -e 's#(\]\(|src="|href=")docs/#\1#g' \
    -e "s#(\\]\\(|href=\")(demo/|LICENSE|CONTRIBUTING\\.md)#\\1${REPO_BLOB}/\\2#g" \
    README.md >"${STAGE}/index.md"

  # Pages under docs/ reach the README as ../README.md and other repo files as ../X.
  for page in "${STAGE}"/*.md; do
    [[ "${page}" == "${STAGE}/index.md" ]] && continue
    sed -E -i.bak \
      -e 's#\]\(\.\./README\.md#](index.md#g' \
      -e "s#\\]\\(\\.\\./#](${REPO_BLOB}/#g" \
      "${page}"
    rm -f "${page}.bak"
  done
  touch "${STAGE}/.staged"
}

# Every recording has to show up somewhere on the site. A GIF that nothing
# links to is a demo nobody will see, so fail instead of shipping without it.
check_media() {
  local missing=0 media
  for media in docs/*.gif docs/*.cast; do
    [[ -e "${media}" ]] || continue
    if ! grep -qF "$(basename "${media}")" "${STAGE}"/*.md; then
      echo "build-docs: ${media} is not referenced by any page (add it to docs/demos.md)" >&2
      missing=1
    fi
  done
  return "${missing}"
}

rm -rf "${STAGE}"
stage
check_media

case "${1:-build}" in
  build)
    exec "${ZENSICAL}" build --clean
    ;;
  serve)
    # Zensical watches the staged copy, so restage whenever a source changes.
    # A file deleted from docs/ stays staged until the next serve.
    (
      while sleep 1; do
        if [[ -n "$(find docs README.md -newer "${STAGE}/.staged" -print -quit)" ]]; then
          stage
        fi
      done
    ) &
    watcher=$!
    "${ZENSICAL}" serve &
    server=$!
    trap 'kill "${watcher}" "${server}" 2>/dev/null' EXIT
    trap 'exit 130' INT TERM
    wait "${server}"
    ;;
  *)
    echo "usage: $0 [build|serve]" >&2
    exit 2
    ;;
esac

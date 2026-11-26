#!/usr/bin/env bash

# Stages docs/ into a Zensical source tree and builds the site.
#
# The Markdown stays written for GitHub, so links out of docs/ to repo files
# (../CONTRIBUTING.md, ../../demo/README.md) point at the file's place in the
# repo. The site doesn't carry those files, so they are rewritten here to their
# GitHub URLs instead of in the sources. docs/index.md is the site's landing
# page; README.md stays the GitHub one.
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

  # A page reaches the repo root through one ../ per level of nesting, plus
  # one to leave docs/. Anything that climbs that far is a repo file.
  local page rel depth up
  while IFS= read -r -d '' page; do
    rel="${page#"${STAGE}"/}"
    depth="$(tr -cd '/' <<<"${rel}" | wc -c)"
    up="$(printf '\.\./%.0s' $(seq 0 "${depth}"))"
    sed -E -i.bak \
      -e "s#\]\(${up}#](${REPO_BLOB}/#g" \
      "${page}"
    rm -f "${page}.bak"
  done < <(find "${STAGE}" -name '*.md' -print0)

  # Served as /install.sh, so the one-line install has a short URL.
  cp scripts/install.sh "${STAGE}/install.sh"
  touch "${STAGE}/.staged"
}

# Every recording has to show up somewhere on the site. A GIF that nothing
# links to is a demo nobody will see, so fail instead of shipping without it.
check_media() {
  local missing=0 media
  for media in docs/*.gif docs/*.cast; do
    [[ -e "${media}" ]] || continue
    if ! grep -rqF --include='*.md' "$(basename "${media}")" "${STAGE}"; then
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
        if [[ -n "$(find docs -newer "${STAGE}/.staged" -print -quit)" ]]; then
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

#!/bin/sh

# Installs the latest AgentBridge release into ~/.local/bin, no root needed.
#
#   curl -fsSL https://agentbridge.sergiogimenez.com/install.sh | sh
#
# AGB_VERSION=v2.0.0 pins a release; AGB_INSTALL_DIR changes where agb goes.
# To build from a checkout instead, run `make install`.

set -eu

REPO="sergio-gimenez/agent-bridge"
BIN_DIR="${AGB_INSTALL_DIR:-${HOME}/.local/bin}"

fail() {
  echo "agb install: $*" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "no release for $(uname -s); build from source with make install" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "no release for $(uname -m); build from source with make install" ;;
esac

if [ -n "${AGB_VERSION:-}" ]; then
  base="https://github.com/${REPO}/releases/download/${AGB_VERSION}"
else
  base="https://github.com/${REPO}/releases/latest/download"
fi
archive="agb_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

echo "Downloading ${archive}"
curl -fsSL -o "${tmp}/${archive}" "${base}/${archive}" || fail "download failed: ${base}/${archive}"
curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt" || fail "download failed: ${base}/checksums.txt"

expected="$(awk -v f="${archive}" '$2 == f { print $1 }' "${tmp}/checksums.txt")"
[ -n "${expected}" ] || fail "${archive} is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${tmp}/${archive}" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "${tmp}/${archive}" | awk '{ print $1 }')"
fi
[ "${actual}" = "${expected}" ] || fail "checksum mismatch for ${archive}"

tar -xzf "${tmp}/${archive}" -C "${tmp}" agb
mkdir -p "${BIN_DIR}"
# Rename into place, so a running agb is never overwritten mid-session.
mv -f "${tmp}/agb" "${BIN_DIR}/agb.new"
chmod 755 "${BIN_DIR}/agb.new"
mv -f "${BIN_DIR}/agb.new" "${BIN_DIR}/agb"
rm -f "${BIN_DIR}/ocs" "${BIN_DIR}/ocsessions"

echo "Installed $("${BIN_DIR}/agb" version) at ${BIN_DIR}/agb"

case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    # shellcheck disable=SC2016 # $PATH is printed for the user to paste.
    printf '\n%s is not on your PATH. Add it to your shell profile:\n  export PATH="%s:$PATH"\n' "${BIN_DIR}" "${BIN_DIR}"
    ;;
esac

#!/usr/bin/env bash
# Install strike from the latest GitHub Release into ~/.strike/bin.
# Usage:
#   curl -fsSL https://strike.jonathanung.ca/install | bash
#   curl -fsSL https://raw.githubusercontent.com/jonathanung/strike-cli/main/scripts/install.sh | bash
# Flags (env or args):
#   --no-modify-path   do not append PATH export to shell rc
#   STRIKE_VERSION=vX  install a specific tag instead of latest
set -euo pipefail

REPO_OWNER="${STRIKE_REPO_OWNER:-jonathanung}"
REPO_NAME="${STRIKE_REPO_NAME:-strike-cli}"
INSTALL_DIR="${STRIKE_INSTALL_DIR:-${HOME}/.strike/bin}"
MODIFY_PATH=1
VERSION="${STRIKE_VERSION:-}"

for arg in "$@"; do
  case "$arg" in
    --no-modify-path) MODIFY_PATH=0 ;;
    --version=*) VERSION="${arg#--version=}" ;;
    -h|--help)
      cat <<'HELP'
Install strike (no root required).

  curl -fsSL https://strike.jonathanung.ca/install | bash

Options:
  --no-modify-path   skip editing ~/.bashrc / ~/.zshrc
  --version=vX.Y.Z   install a specific release tag
HELP
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "strike install: missing required command: $1" >&2
    exit 1
  }
}
need curl
need tar
need mktemp

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in
  linux|darwin) ;;
  *)
    echo "strike install: unsupported OS: $os" >&2
    exit 1
    ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *)
    echo "strike install: unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

api="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases"
if [ -n "$VERSION" ]; then
  release_url="${api}/tags/${VERSION}"
else
  release_url="${api}/latest"
fi

echo "resolving release from ${release_url}"
json="$(curl -fsSL -H 'Accept: application/vnd.github+json' -H 'User-Agent: strike-install' "$release_url")" || {
  echo "strike install: failed to fetch release metadata (offline or no releases yet)" >&2
  exit 1
}

tag="$(printf '%s' "$json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
if [ -z "$tag" ]; then
  echo "strike install: could not parse tag_name from release JSON" >&2
  exit 1
fi

asset="strike_${tag}_${os}_${arch}.tar.gz"
# Prefer browser_download_url from the JSON; fall back to releases/download path.
asset_url="$(printf '%s' "$json" | tr '{' '\n' | grep -F "\"name\": \"${asset}\"" -A6 | sed -n 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
if [ -z "$asset_url" ]; then
  asset_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${tag}/${asset}"
fi
sums_url="$(printf '%s' "$json" | tr '{' '\n' | grep -F '"name": "checksums.txt"' -A6 | sed -n 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
if [ -z "$sums_url" ]; then
  sums_url="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${tag}/checksums.txt"
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

echo "downloading ${asset}"
curl -fsSL -o "${tmpdir}/${asset}" "$asset_url" || {
  echo "strike install: download failed for ${asset_url}" >&2
  exit 1
}
echo "downloading checksums.txt"
curl -fsSL -o "${tmpdir}/checksums.txt" "$sums_url" || {
  echo "strike install: download failed for ${sums_url}" >&2
  exit 1
}

want="$(awk -v f="$asset" '$2 == f || $2 == ("*" f) || $2 ~ ("/" f "$") {print tolower($1); exit}' "${tmpdir}/checksums.txt")"
if [ -z "$want" ]; then
  echo "strike install: checksums.txt has no entry for ${asset}" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "${tmpdir}/${asset}" | awk '{print tolower($1)}')"
elif command -v shasum >/dev/null 2>&1; then
  got="$(shasum -a 256 "${tmpdir}/${asset}" | awk '{print tolower($1)}')"
else
  echo "strike install: need sha256sum or shasum" >&2
  exit 1
fi
if [ "$got" != "$want" ]; then
  echo "strike install: checksum mismatch for ${asset}" >&2
  echo "  got  ${got}" >&2
  echo "  want ${want}" >&2
  exit 1
fi

tar -C "$tmpdir" -xzf "${tmpdir}/${asset}"
if [ ! -f "${tmpdir}/strike" ]; then
  echo "strike install: archive did not contain strike binary" >&2
  exit 1
fi
chmod 755 "${tmpdir}/strike"

mkdir -p "$INSTALL_DIR"
# Atomic replace into install dir.
install_path="${INSTALL_DIR}/strike"
tmp_bin="${INSTALL_DIR}/.strike-install-$$"
mv "${tmpdir}/strike" "$tmp_bin"
mv "$tmp_bin" "$install_path"
echo "installed ${install_path} (${tag})"

path_line='export PATH="$HOME/.strike/bin:$PATH"'
# Prefer INSTALL_DIR-relative home form when under $HOME/.strike/bin
if [ "$INSTALL_DIR" != "${HOME}/.strike/bin" ]; then
  path_line="export PATH=\"${INSTALL_DIR}:\$PATH\""
fi

append_path_line() {
  local rc="$1"
  [ -f "$rc" ] || touch "$rc"
  if grep -Fqs '.strike/bin' "$rc" 2>/dev/null || grep -Fqs "$INSTALL_DIR" "$rc" 2>/dev/null; then
    echo "PATH already configured in ${rc}"
    return
  fi
  printf '\n# strike\n%s\n' "$path_line" >>"$rc"
  echo "added PATH line to ${rc}"
}

if [ "$MODIFY_PATH" -eq 1 ]; then
  case "${SHELL:-}" in
    */zsh) append_path_line "${ZDOTDIR:-$HOME}/.zshrc" ;;
    */bash) append_path_line "$HOME/.bashrc" ;;
    *)
      if [ -f "$HOME/.zshrc" ]; then
        append_path_line "$HOME/.zshrc"
      elif [ -f "$HOME/.bashrc" ]; then
        append_path_line "$HOME/.bashrc"
      else
        echo "add to your shell rc: ${path_line}"
      fi
      ;;
  esac
  echo
  echo "restart your shell or run: ${path_line}"
fi

echo "verify: strike version"
if [ -x "$install_path" ]; then
  "$install_path" version || true
fi

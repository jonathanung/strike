#!/usr/bin/env bash
# Render package manager manifests from packaging/*.in for one release tag.
#
#   scripts/render-packaging.sh <tag> <checksums.txt> <out-dir>
#
# <checksums.txt> is the release asset (sha256sum output over
# strike_<tag>_<os>_<arch>.tar.gz). Writes:
#   <out-dir>/homebrew/strike.rb
#   <out-dir>/aur/PKGBUILD
# See packaging/README.md for the manual publish steps.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <tag> <checksums.txt> <out-dir>" >&2
  exit 2
fi
tag="$1"
sums="$2"
out="$3"

if ! [[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  echo "render-packaging: unexpected tag ${tag}" >&2
  exit 1
fi
if [ ! -f "${sums}" ]; then
  echo "render-packaging: checksums file not found: ${sums}" >&2
  exit 1
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${tag#v}"
pkgver="${version//-/_}"

# sha_for <os> <arch>: the digest of strike_<tag>_<os>_<arch>.tar.gz.
sha_for() {
  local name="strike_${tag}_$1_$2.tar.gz" digest
  digest="$(awk -v n="${name}" '{ f = $2; sub(/^\*/, "", f) } f == n { print $1; exit }' "${sums}")"
  if ! [[ "${digest}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "render-packaging: no sha256 for ${name} in ${sums}" >&2
    return 1
  fi
  printf '%s' "${digest}"
}

darwin_arm64="$(sha_for darwin arm64)"
darwin_amd64="$(sha_for darwin amd64)"
linux_arm64="$(sha_for linux arm64)"
linux_amd64="$(sha_for linux amd64)"

render() {
  local src="$1" dst="$2"
  mkdir -p "$(dirname "${dst}")"
  sed \
    -e "s/@TAG@/${tag}/g" \
    -e "s/@VERSION@/${version}/g" \
    -e "s/@PKGVER@/${pkgver}/g" \
    -e "s/@SHA256_DARWIN_ARM64@/${darwin_arm64}/g" \
    -e "s/@SHA256_DARWIN_AMD64@/${darwin_amd64}/g" \
    -e "s/@SHA256_LINUX_ARM64@/${linux_arm64}/g" \
    -e "s/@SHA256_LINUX_AMD64@/${linux_amd64}/g" \
    "${src}" > "${dst}"
  if grep -En '@[A-Z0-9_]+@' "${dst}" >&2; then
    echo "render-packaging: unfilled placeholder in ${dst}" >&2
    return 1
  fi
}

render "${root}/packaging/homebrew/strike.rb.in" "${out}/homebrew/strike.rb"
render "${root}/packaging/aur/PKGBUILD.in" "${out}/aur/PKGBUILD"
echo "rendered ${tag} manifests into ${out}"

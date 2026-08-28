#!/usr/bin/env bash
# Recompute flake.nix's buildGoModule vendorHash after go.mod/go.sum change.
# Only realises the go-modules fixed-output derivation (.#default.goModules),
# never compiles strike, so it's cheap. Prints the old -> new hash on change.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
flake="${repo_root}/flake.nix"

current_hash="$(
  sed -nE 's/^[[:space:]]*vendorHash = "([^"]+)";[[:space:]]*$/\1/p' "${flake}" | head -n1
)"

if [[ -z "${current_hash}" ]]; then
  echo "could not find quoted vendorHash in ${flake}" >&2
  exit 1
fi

restore_current_hash() {
  # NOTE: | delimiter because vendor hashes are base64 and may contain /,
  # which would terminate an s/// substitution early.
  perl -0pi -e "s|vendorHash = nixpkgs\\.lib\\.fakeHash;|vendorHash = \"${current_hash}\";|" "${flake}"
}

perl -0pi -e 's/vendorHash = "[^"]+";/vendorHash = nixpkgs.lib.fakeHash;/' "${flake}"
trap restore_current_hash EXIT

set +e
build_output="$(cd "${repo_root}" && nix build .#default.goModules --no-link 2>&1)"
build_status=$?
set -e

new_hash="$(
  printf '%s\n' "${build_output}" |
    sed -nE 's/^[[:space:]]*got:[[:space:]]*(sha256-[A-Za-z0-9+/=]+)[[:space:]]*$/\1/p' |
    tail -n1
)"

if [[ -z "${new_hash}" ]]; then
  printf '%s\n' "${build_output}" >&2
  echo "nix did not report a replacement vendorHash" >&2
  exit "${build_status}"
fi

trap - EXIT
perl -0pi -e "s|vendorHash = nixpkgs\\.lib\\.fakeHash;|vendorHash = \"${new_hash}\";|" "${flake}"

if [[ "${new_hash}" == "${current_hash}" ]]; then
  echo "vendorHash already current: ${current_hash}"
else
  echo "updated vendorHash: ${current_hash} -> ${new_hash}"
fi

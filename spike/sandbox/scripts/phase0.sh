#!/usr/bin/env bash
# THROWAWAY. Build the microvm guest and run one TCG proof. See ../README.md.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
spike=$(cd "$here/.." && pwd)
root=$(cd "$spike/../.." && pwd)
if [[ -f "$HOME/.nix-profile/etc/profile.d/nix.sh" ]]; then
  # shellcheck disable=SC1091
  . "$HOME/.nix-profile/etc/profile.d/nix.sh"
fi
cd "$spike"
nix build --print-build-logs --out-link result ".#default"
cd "$root"
go run ./spike/sandbox/driver inspect --runner "$spike/result"
go run ./spike/sandbox/driver run --runner "$spike/result"

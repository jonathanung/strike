#!/usr/bin/env bash
# Restore ~/.strike (and optionally ./.strike) directory structure when metadata
# is missing or corrupted. Safe to re-run: valid files are never overwritten.
# Corrupt JSON is moved to <name>.corrupt-<timestamp>.
#
# Prefer `strike restore` when the binary is available (same behavior, tested).
# This script is a no-binary fallback aligned with scripts/setup.sh.
set -euo pipefail

PROJECT=0
PROJECT_DIR=""

usage() {
  cat <<'EOF'
Usage: restore.sh [--project] [--project-dir <path>]

  Restores ~/.strike directories and default config. With --project, also
  restores ./.strike (or --project-dir). Valid files are kept; corrupt JSON
  metadata is quarantined as <name>.corrupt-<timestamp>.

  Prefer: strike restore [--project] [--project-dir <path>]
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --project)
      PROJECT=1
      shift
      ;;
    --project-dir)
      if [ $# -lt 2 ]; then
        echo "restore.sh: --project-dir requires a path" >&2
        exit 2
      fi
      PROJECT=1
      PROJECT_DIR="$2"
      shift 2
      ;;
    --project-dir=*)
      PROJECT=1
      PROJECT_DIR="${1#--project-dir=}"
      shift
      ;;
    *)
      echo "restore.sh: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

# If strike is on PATH, prefer the Go implementation (single source of truth).
if command -v strike >/dev/null 2>&1; then
  args=(restore)
  if [ "$PROJECT" -eq 1 ]; then
    if [ -n "$PROJECT_DIR" ]; then
      args+=(--project-dir "$PROJECT_DIR")
    else
      args+=(--project)
    fi
  fi
  exec strike "${args[@]}"
fi

STRIKE_DIR="${HOME}/.strike"
TS="$(date -u +%Y%m%d-%H%M%S 2>/dev/null || date +%Y%m%d-%H%M%S)"

created=0
quarantined=0
kept=0

is_valid_json() {
  local path="$1"
  # empty is ok
  if [ ! -s "$path" ]; then
    return 0
  fi
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys, re
p = sys.argv[1]
raw = open(p, "r", encoding="utf-8").read()
# strip // and /* */ outside strings (simple)
out = []
i = 0
n = len(raw)
in_str = False
esc = False
while i < n:
    c = raw[i]
    if in_str:
        out.append(c)
        if esc:
            esc = False
        elif c == "\\":
            esc = True
        elif c == '"':
            in_str = False
        i += 1
        continue
    if c == '"':
        in_str = True
        out.append(c)
        i += 1
        continue
    if c == "/" and i + 1 < n and raw[i + 1] == "/":
        i += 2
        while i < n and raw[i] != "\n":
            i += 1
        continue
    if c == "/" and i + 1 < n and raw[i + 1] == "*":
        i += 2
        while i + 1 < n and not (raw[i] == "*" and raw[i + 1] == "/"):
            i += 1
        i = min(i + 2, n)
        continue
    out.append(c)
    i += 1
text = "".join(out).strip()
if not text:
    sys.exit(0)
try:
    json.loads(text)
except Exception:
    sys.exit(1)
PY
    return $?
  fi
  # No python: treat non-empty files as kept (avoid false quarantine).
  return 0
}

ensure_dir() {
  local path="$1"
  local mode="${2:-755}"
  if [ -d "$path" ]; then
    kept=$((kept + 1))
    return 0
  fi
  if [ -e "$path" ]; then
    local bak="${path}.corrupt-${TS}"
    mv "$path" "$bak"
    echo "  quarantined ${path} → ${bak} (expected directory)"
    quarantined=$((quarantined + 1))
  fi
  mkdir -p "$path"
  chmod "$mode" "$path" 2>/dev/null || true
  echo "  created      ${path}"
  created=$((created + 1))
}

quarantine_if_bad_json() {
  local path="$1"
  local rewrite_default="${2:-}"
  if [ ! -e "$path" ]; then
    if [ -n "$rewrite_default" ]; then
      printf '%s' "$rewrite_default" >"$path"
      echo "  created      ${path}"
      created=$((created + 1))
    fi
    return 0
  fi
  if [ -d "$path" ]; then
    local bak="${path}.corrupt-${TS}"
    mv "$path" "$bak"
    echo "  quarantined ${path} → ${bak} (expected file)"
    quarantined=$((quarantined + 1))
    if [ -n "$rewrite_default" ]; then
      printf '%s' "$rewrite_default" >"$path"
      echo "  created      ${path}"
      created=$((created + 1))
    fi
    return 0
  fi
  if is_valid_json "$path"; then
    kept=$((kept + 1))
    return 0
  fi
  local bak="${path}.corrupt-${TS}"
  mv "$path" "$bak"
  echo "  quarantined ${path} → ${bak} (invalid JSON)"
  quarantined=$((quarantined + 1))
  if [ -n "$rewrite_default" ]; then
    printf '%s' "$rewrite_default" >"$path"
    echo "  created      ${path}"
    created=$((created + 1))
  fi
}

DEFAULT_CONFIG='{
  "provider": "anthropic",
  "defaultAgent": "build"
}
'

restore_global() {
  local root="$1"
  echo "strike restore: ${root}"
  if [ ! -d "$root" ]; then
    if [ -e "$root" ]; then
      echo "restore.sh: ${root} exists and is not a directory" >&2
      exit 1
    fi
    mkdir -p "$root"
    chmod 700 "$root" 2>/dev/null || true
    echo "  created      ${root}"
    created=$((created + 1))
  else
    kept=$((kept + 1))
  fi

  ensure_dir "${root}/agents" 755
  ensure_dir "${root}/skills" 755
  ensure_dir "${root}/sessions" 755
  ensure_dir "${root}/history" 700
  ensure_dir "${root}/memory" 700
  ensure_dir "${root}/issues" 700
  ensure_dir "${root}/goals" 700
  ensure_dir "${root}/cache" 755
  ensure_dir "${root}/themes" 755
  ensure_dir "${root}/workflows" 755
  ensure_dir "${root}/bin" 755

  quarantine_if_bad_json "${root}/config" "$DEFAULT_CONFIG"
  for f in mcp.jsonc mcp.json providers.jsonc providers.json keybinds.jsonc keybinds.json auth.json; do
    if [ -e "${root}/${f}" ]; then
      quarantine_if_bad_json "${root}/${f}" ""
    fi
  done

  local skill="${root}/skills/commit.md"
  if [ -e "$skill" ]; then
    kept=$((kept + 1))
  else
    cat >"$skill" <<'EOF'
---
description: stage and commit the current changes with a good message
---
Look at the uncommitted changes (git status, git diff), stage the relevant
files, and commit them with a concise, descriptive message. $ARGUMENTS
EOF
    echo "  created      ${skill}"
    created=$((created + 1))
  fi

  if [ "$created" -eq 0 ] && [ "$quarantined" -eq 0 ]; then
    echo "  (nothing to fix; structure ok)"
  fi
  echo "  summary: ${created} created, ${quarantined} quarantined, ${kept} kept"
}

restore_project() {
  local root="$1"
  # reset counters for second report block
  created=0
  quarantined=0
  kept=0
  echo "strike restore: ${root}"
  if [ ! -d "$root" ]; then
    if [ -e "$root" ]; then
      echo "restore.sh: ${root} exists and is not a directory" >&2
      exit 1
    fi
    mkdir -p "$root"
    echo "  created      ${root}"
    created=$((created + 1))
  else
    kept=$((kept + 1))
  fi
  ensure_dir "${root}/agents" 755
  ensure_dir "${root}/skills" 755
  ensure_dir "${root}/themes" 755
  ensure_dir "${root}/workflows" 755
  ensure_dir "${root}/worktrees" 755
  ensure_dir "${root}/exports" 755
  quarantine_if_bad_json "${root}/config" "$DEFAULT_CONFIG"
  for f in mcp.jsonc mcp.json providers.jsonc providers.json keybinds.jsonc keybinds.json; do
    if [ -e "${root}/${f}" ]; then
      quarantine_if_bad_json "${root}/${f}" ""
    fi
  done
  if [ "$created" -eq 0 ] && [ "$quarantined" -eq 0 ]; then
    echo "  (nothing to fix; structure ok)"
  fi
  echo "  summary: ${created} created, ${quarantined} quarantined, ${kept} kept"
}

restore_global "$STRIKE_DIR"

if [ "$PROJECT" -eq 1 ]; then
  base="${PROJECT_DIR:-.}"
  restore_project "${base%/}/.strike"
fi

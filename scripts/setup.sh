#!/usr/bin/env bash
# Sets up ~/.strike: config with defaults, agents/ and skills/ folders with
# starter examples. Safe to re-run — existing files are never overwritten.
set -euo pipefail

STRIKE_DIR="${HOME}/.strike"
mkdir -p "${STRIKE_DIR}/agents" "${STRIKE_DIR}/skills" "${STRIKE_DIR}/sessions"

created=()
skipped=()

write_if_absent() {
  local path="$1"
  if [ -e "${path}" ]; then
    skipped+=("${path}")
  else
    cat > "${path}"
    created+=("${path}")
  fi
}

write_if_absent "${STRIKE_DIR}/config" <<'EOF'
{
  "provider": "anthropic",
  "defaultAgent": "build"
}
EOF

# plan is built into strike; a user plan.md here overrides the built-in.

write_if_absent "${STRIKE_DIR}/skills/commit.md" <<'EOF'
---
description: stage and commit the current changes with a good message
---
Look at the uncommitted changes (git status, git diff), stage the relevant
files, and commit them with a concise, descriptive message. $ARGUMENTS
EOF

echo "strike home: ${STRIKE_DIR}"
for f in "${created[@]:-}"; do [ -n "$f" ] && echo "  created  $f"; done
for f in "${skipped[@]:-}"; do [ -n "$f" ] && echo "  kept     $f (already exists)"; done
echo
echo "Edit ${STRIKE_DIR}/config to change defaults, or press ctrl+d in the"
echo "TUI to save the current provider/model/agent as your defaults."

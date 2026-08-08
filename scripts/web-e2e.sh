#!/usr/bin/env bash
# Boot offline echo-backed strike serve fixtures and run Playwright smokes (WEBUI.5).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ ! -f web/package.json ]]; then
  echo "web-e2e: no web/package.json" >&2
  exit 0
fi

LIVE_PORT="${STRIKE_E2E_LIVE_PORT:-8791}"
ATTACH_PORT="${STRIKE_E2E_ATTACH_PORT:-8792}"
HOME_DIR="${STRIKE_E2E_HOME:-$(mktemp -d -t strike-e2e-home.XXXXXX)}"
SESSION_DIR="${STRIKE_E2E_SESSION_DIR:-$HOME_DIR/.strike/sessions}"
ARTIFACT_DIR="${STRIKE_E2E_ARTIFACT_DIR:-$ROOT/web/e2e-artifacts}"
mkdir -p "$HOME_DIR" "$SESSION_DIR" "$ARTIFACT_DIR"

export HOME="$HOME_DIR"
export XDG_CONFIG_HOME="$HOME_DIR/.config"
export XDG_STATE_HOME="$HOME_DIR/.local/state"
export XDG_CACHE_HOME="$HOME_DIR/.cache"

cleanup() {
  local code=$?
  if [[ -n "${LIVE_PID:-}" ]] && kill -0 "$LIVE_PID" 2>/dev/null; then
    kill "$LIVE_PID" 2>/dev/null || true
    wait "$LIVE_PID" 2>/dev/null || true
  fi
  if [[ -n "${ATTACH_PID:-}" ]] && kill -0 "$ATTACH_PID" 2>/dev/null; then
    kill "$ATTACH_PID" 2>/dev/null || true
    wait "$ATTACH_PID" 2>/dev/null || true
  fi
  exit "$code"
}
trap cleanup EXIT INT TERM

echo "web-e2e: building web assets then strike binary (embed order) (HOME=$HOME_DIR)"
# static assets are go:embed — web-build must precede build.
make web-build build

BIN="$ROOT/strike"
if [[ ! -x "$BIN" ]]; then
  echo "web-e2e: missing ./strike binary" >&2
  exit 1
fi

LIVE_LOG="$ARTIFACT_DIR/live-serve.log"
ATTACH_LOG="$ARTIFACT_DIR/attach-serve.log"

echo "web-e2e: starting live echo server on :$LIVE_PORT"
"$BIN" serve \
  --addr "127.0.0.1:$LIVE_PORT" \
  --provider echo \
  >"$LIVE_LOG" 2>&1 &
LIVE_PID=$!

echo "web-e2e: starting attach-only server on :$ATTACH_PORT"
"$BIN" serve \
  --addr "127.0.0.1:$ATTACH_PORT" \
  --attach-only \
  --session-dir "$SESSION_DIR" \
  >"$ATTACH_LOG" 2>&1 &
ATTACH_PID=$!

wait_http() {
  local url=$1
  local name=$2
  for _ in $(seq 1 60); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      echo "web-e2e: $name ready ($url)"
      return 0
    fi
    sleep 0.25
  done
  echo "web-e2e: $name failed to become ready: $url" >&2
  [[ -f "$LIVE_LOG" ]] && tail -n 80 "$LIVE_LOG" >&2 || true
  [[ -f "$ATTACH_LOG" ]] && tail -n 80 "$ATTACH_LOG" >&2 || true
  return 1
}

wait_http "http://127.0.0.1:$LIVE_PORT/health" "live"
wait_http "http://127.0.0.1:$ATTACH_PORT/health" "attach-only"

cd web
if [[ ! -d node_modules ]]; then
  npm ci
else
  # Ensure lockfile deps (including playwright) are present.
  npm ci
fi

if [[ ! -d node_modules/@playwright/test ]]; then
  echo "web-e2e: @playwright/test missing after npm ci" >&2
  exit 1
fi

export STRIKE_E2E_BASE="http://127.0.0.1:$LIVE_PORT"
export STRIKE_E2E_ATTACH_BASE="http://127.0.0.1:$ATTACH_PORT/attach"
export CI="${CI:-}"
# Keep browser cache outside the disposable HOME so installs stick across runs.
export PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-$ROOT/web/.pw-browsers}"
mkdir -p "$PLAYWRIGHT_BROWSERS_PATH"

# Install browser once (CI images usually need --with-deps).
if [[ -n "${CI:-}" ]]; then
  npx playwright install chromium --with-deps
else
  npx playwright install chromium
fi

echo "web-e2e: running Playwright (base=$STRIKE_E2E_BASE attach=$STRIKE_E2E_ATTACH_BASE)"
set +e
npx playwright test "$@"
code=$?
set -e

# Retain traces/screenshots/report under artifact dir for CI upload.
mkdir -p "$ARTIFACT_DIR"
if [[ -d e2e-results ]]; then
  rm -rf "$ARTIFACT_DIR/e2e-results"
  cp -a e2e-results "$ARTIFACT_DIR/e2e-results"
fi
if [[ -d e2e-report ]]; then
  rm -rf "$ARTIFACT_DIR/e2e-report"
  cp -a e2e-report "$ARTIFACT_DIR/e2e-report"
fi

exit "$code"

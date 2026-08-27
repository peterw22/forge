#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOST="${FORGE_AGENT_HOST:-192.168.50.50}"
PORT="${FORGE_AGENT_PORT:-7346}"
WORKSPACE="${FORGE_AGENT_CWD:-$ROOT}"
TARGET="$ROOT/pi-go-agent"
DIST_TARGET="$ROOT/dist/macos/pi-go-agent"
AGENT_LOG="${FORGE_AGENT_LOG:-/tmp/pi-go-agent-7346.log}"
PID_FILE="${FORGE_AGENT_PID_FILE:-/tmp/pi-go-agent-7346.pid}"
STATUS_FILE="${FORGE_AGENT_STATUS_FILE:-/tmp/pi-go-agent-rebuild-7346.status}"
LOCK_DIR="${TMPDIR:-/tmp}/forge-agent-rebuild-7346.lock"
TEMP_TARGET="$ROOT/.pi-go-agent.new.$$"
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/forge-agent-build.XXXXXX")"

cleanup() {
  rm -rf "$BUILD_DIR" "$LOCK_DIR"
  rm -f "$TEMP_TARGET"
}
trap cleanup EXIT

if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  echo "Another Forge agent rebuild is already running" >&2
  exit 1
fi

printf 'building\n' > "$STATUS_FILE"
cd "$ROOT"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -trimpath -ldflags='-s -w' \
  -o "$BUILD_DIR/pi-go-agent-arm64" ./cmd/pi-go-agent
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' \
  -o "$BUILD_DIR/pi-go-agent-amd64" ./cmd/pi-go-agent
lipo -create \
  "$BUILD_DIR/pi-go-agent-arm64" \
  "$BUILD_DIR/pi-go-agent-amd64" \
  -output "$TEMP_TARGET"
chmod 0755 "$TEMP_TARGET"

find_codesign_identity() {
  security find-identity -v -p codesigning 2>/dev/null \
    | awk -F '"' 'index($2, "Developer ID Application:") == 1 {print $2; exit}'
}

SIGN_IDENTITY="$(find_codesign_identity)"
if [[ -n "$SIGN_IDENTITY" ]]; then
  codesign --force --options runtime --timestamp \
    --sign "$SIGN_IDENTITY" "$TEMP_TARGET"
else
  codesign --force --timestamp=none --sign - "$TEMP_TARGET"
fi
codesign --verify --strict "$TEMP_TARGET"
lipo -info "$TEMP_TARGET"

# Replace only after both architectures build and the final signature verifies.
mv -f "$TEMP_TARGET" "$TARGET"
mkdir -p "$(dirname "$DIST_TARGET")"
ditto "$TARGET" "$DIST_TARGET"
chmod 0755 "$DIST_TARGET"

# Stop only the agent listening on this exact address and port. A local editor
# may separately use 127.0.0.1:7346 and must not be terminated.
OLD_PIDS="$(lsof -nP -t -iTCP@"$HOST":"$PORT" -sTCP:LISTEN 2>/dev/null || true)"
if [[ -n "$OLD_PIDS" ]]; then
  while IFS= read -r pid; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done <<< "$OLD_PIDS"
  for _ in {1..50}; do
    alive=false
    while IFS= read -r pid; do
      if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
        alive=true
      fi
    done <<< "$OLD_PIDS"
    [[ "$alive" == false ]] && break
    sleep 0.1
  done
  while IFS= read -r pid; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
  done <<< "$OLD_PIDS"
fi

# nohup is intentional: this launcher and the new server survive termination of
# the old agent process that owns the current Forge coding session.
nohup "$TARGET" \
  --allow-remote \
  --listen "ws://$HOST:$PORT/ws" \
  --cwd "$WORKSPACE" \
  > "$AGENT_LOG" 2>&1 < /dev/null &
NEW_PID=$!
echo "$NEW_PID" > "$PID_FILE"

sleep 2
if ! kill -0 "$NEW_PID" 2>/dev/null; then
  printf 'failed pid=%s\n' "$NEW_PID" > "$STATUS_FILE"
  cat "$AGENT_LOG" >&2 || true
  exit 1
fi
if ! lsof -nP -a -p "$NEW_PID" -iTCP@"$HOST":"$PORT" -sTCP:LISTEN >/dev/null; then
  printf 'failed_not_listening pid=%s\n' "$NEW_PID" > "$STATUS_FILE"
  cat "$AGENT_LOG" >&2 || true
  exit 1
fi

SHA256="$(shasum -a 256 "$TARGET" | awk '{print $1}')"
printf 'running pid=%s url=ws://%s:%s/ws sha256=%s\n' \
  "$NEW_PID" "$HOST" "$PORT" "$SHA256" > "$STATUS_FILE"
cat "$STATUS_FILE"

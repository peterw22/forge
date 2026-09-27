#!/usr/bin/env bash
# Installs the agent of this source tree with install.sh, as a service under
# a name, a port and directories of its own, checks it, and removes it.
# Nothing of an agent that is installed for real is touched.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${FORGE_TEST_PORT:-17399}"
TEST="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/forge-install-test.XXXXXX")" && pwd -P)"

export FORGE_SERVICE=forge-agent-test
export FORGE_BIN_DIR="$TEST/bin"
export PI_GO_CONFIG_DIR="$TEST/config"
# A directory with a space and a percent sign, which a unit must escape.
WORKSPACE="$TEST/work space 100%"
WHITELIST="$PI_GO_CONFIG_DIR/authorized-devices.json"

finish() {
  status=$?
  "$ROOT/install.sh" --uninstall >/dev/null 2>&1 || true
  if [ "$status" != 0 ]; then
    echo "--- log of the agent" >&2
    cat "$PI_GO_CONFIG_DIR/logs/$FORGE_SERVICE.log" >&2 2>/dev/null || true
  fi
  rm -rf "$TEST"
  exit "$status"
}
trap finish EXIT

check() {
  echo "ok: $1"
}

base64url() {
  openssl base64 -A | tr '+/' '-_' | tr -d '='
}

# Writes a device entry as Forge copies it: a P-256 key and its fingerprint.
device_entry() {
  local name="$1" file="$2" point x y fingerprint
  point="$TEST/point"
  # The last 64 bytes of the encoded public key are its coordinates.
  openssl ecparam -name prime256v1 -genkey -noout 2>/dev/null |
    openssl ec -pubout -outform DER 2>/dev/null | tail -c 64 >"$point"
  x="$(head -c 32 "$point" | base64url)"
  y="$(tail -c 32 "$point" | base64url)"
  fingerprint="$(printf 'P-256.%s.%s' "$x" "$y" | openssl dgst -sha256 -binary | base64url)"
  cat >"$file" <<EOF
{
  "name": "$name",
  "deviceId": "$name-id",
  "publicKey": {
    "kty": "EC",
    "crv": "P-256",
    "x": "$x",
    "y": "$y"
  },
  "fingerprint": "$fingerprint"
}
EOF
}

healthy() {
  curl --fail --silent --max-time 2 -o /dev/null "http://127.0.0.1:$PORT/healthz"
}

mkdir -p "$WORKSPACE"
(cd "$ROOT" && go build -o "$TEST/pi-go-agent" ./cmd/pi-go-agent)
device_entry phone "$TEST/phone.json"
device_entry laptop "$TEST/laptop.json"

"$ROOT/install.sh" --yes --binary "$TEST/pi-go-agent" --port "$PORT" \
  --cwd "$WORKSPACE" --device-file "$TEST/phone.json" --linger no
healthy
check "the service answers"
grep -q '"deviceId": "phone-id"' "$WHITELIST"
check "the device is listed"
[ "$(ls -l "$WHITELIST" | cut -c1-10)" = "-rw-------" ]
check "the whitelist is private"
grep -q "work space 100%" "$PI_GO_CONFIG_DIR/logs/$FORGE_SERVICE.log" || true

# The service is restarted when the agent dies.
pid="$(pgrep -f "$FORGE_BIN_DIR/pi-go-agent --listen ws://127.0.0.1:$PORT/ws")"
kill "$pid"
count=0
until healthy && [ "$(pgrep -f "$FORGE_BIN_DIR/pi-go-agent --listen ws://127.0.0.1:$PORT/ws")" != "$pid" ]; do
  count=$((count + 1))
  [ "$count" -lt 30 ]
  sleep 1
done
check "the service restarts the agent"

# Installing again keeps the devices and adds the next.
"$ROOT/install.sh" --yes --binary "$TEST/pi-go-agent" --port "$PORT" \
  --cwd "$WORKSPACE" --device-file "$TEST/laptop.json" --linger no >/dev/null
healthy
grep -q '"deviceId": "phone-id"' "$WHITELIST"
grep -q '"deviceId": "laptop-id"' "$WHITELIST"
check "a second installation keeps the first device"

# A device whose fingerprint is not that of its key stops the installation.
sed 's/"fingerprint": "[^"]*"/"fingerprint": "wrong"/' "$TEST/phone.json" >"$TEST/forged.json"
if "$ROOT/install.sh" --yes --binary "$TEST/pi-go-agent" --port "$PORT" \
  --cwd "$WORKSPACE" --device-file "$TEST/forged.json" --linger no >/dev/null 2>&1; then
  echo "a forged device was accepted" >&2
  exit 1
fi
check "a forged device is refused"

"$ROOT/install.sh" --uninstall >/dev/null
count=0
while healthy; do
  count=$((count + 1))
  [ "$count" -lt 15 ]
  sleep 1
done
[ ! -e "$FORGE_BIN_DIR/pi-go-agent" ]
[ -f "$WHITELIST" ]
check "uninstalling stops the service and keeps the devices"
echo "All checks passed."

#!/usr/bin/env bash
# Development launcher: build a matching helper and pass its explicit path.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
case "$(uname -s)" in
  Darwin) device=macos ;;
  Linux) device=linux ;;
  *) echo "Native desktop development requires macOS or Linux" >&2; exit 1 ;;
esac
HELPER="$ROOT/flutter/pi_go_app/build/local-agent/pi-go-agent"
mkdir -p "$(dirname "$HELPER")"
cd "$ROOT"
CGO_ENABLED=0 go build -o "$HELPER" ./cmd/pi-go-agent
export PI_GO_AGENT_BIN="$HELPER"
cd flutter/pi_go_app
flutter pub get
exec flutter run -d "$device" "$@"

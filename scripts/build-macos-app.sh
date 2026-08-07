#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_ROOT="$ROOT/flutter/pi_go_app"
SOURCE_APP="$APP_ROOT/build/macos/Build/Products/Release/Forge.app"
DIST="$ROOT/dist/macos"

if [[ -z "${DEVELOPER_DIR:-}" && -x /Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild ]]; then
  export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
fi
if ! xcrun --find xcodebuild >/dev/null 2>&1; then
  echo "Full Xcode is required. Install Xcode and run: sudo xcode-select -s /Applications/Xcode.app/Contents/Developer" >&2
  exit 1
fi

cd "$APP_ROOT"
flutter pub get
flutter build macos --release

mkdir -p "$DIST"
rm -rf "$DIST/Forge.app" "$DIST/Forge-macOS.zip"
ditto "$SOURCE_APP" "$DIST/Forge.app"
ditto -c -k --sequesterRsrc --keepParent "$DIST/Forge.app" "$DIST/Forge-macOS.zip"

echo "Built $DIST/Forge.app"
echo "Built $DIST/Forge-macOS.zip"

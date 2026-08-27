#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_ROOT="$ROOT/flutter/pi_go_app"
BUILD_ROOT="${FLATPAK_BUILD_ROOT:-$ROOT/build/flatpak-linux}"
PREFIX="${FLATPAK_DEST:-$BUILD_ROOT/files}"

if [[ "$(uname -s)" != Linux ]]; then
  echo "Forge Flatpak must be built on Linux (native, VM, or CI)." >&2
  exit 1
fi
for command in flutter cmake ninja pkg-config; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done

rm -rf "$PREFIX"
cd "$APP_ROOT"
flutter pub get
flutter build linux --release
BUNDLE="$APP_ROOT/build/linux/x64/release/bundle"
if [[ ! -x "$BUNDLE/forge" ]]; then
  echo "Flutter Linux bundle was not produced at $BUNDLE" >&2
  exit 1
fi

install -d "$PREFIX/lib/forge" "$PREFIX/bin" \
  "$PREFIX/share/applications" "$PREFIX/share/metainfo" \
  "$PREFIX/share/icons/hicolor/512x512/apps"
cp -a "$BUNDLE/." "$PREFIX/lib/forge/"
ln -s ../lib/forge/forge "$PREFIX/bin/forge"
install -m 0644 "$ROOT/flatpak/com.tingouw.forge.desktop" \
  "$PREFIX/share/applications/com.tingouw.forge.desktop"
install -m 0644 "$ROOT/flatpak/com.tingouw.forge.metainfo.xml" \
  "$PREFIX/share/metainfo/com.tingouw.forge.metainfo.xml"
install -m 0644 "$APP_ROOT/web/icons/Icon-512.png" \
  "$PREFIX/share/icons/hicolor/512x512/apps/com.tingouw.forge.png"

echo "Prepared Flatpak files in $PREFIX"

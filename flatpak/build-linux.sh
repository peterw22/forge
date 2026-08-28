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

normalize_arch() {
  case "$1" in
    x86_64|amd64|x64|linux-x64) echo x86_64 ;;
    aarch64|arm64|linux-arm64) echo aarch64 ;;
    *) echo "Unsupported Flatpak architecture: $1" >&2; exit 1 ;;
  esac
}

ARCH="$(normalize_arch "${FORGE_FLATPAK_ARCH:-$(uname -m)}")"
case "$ARCH" in
  x86_64)
    FLUTTER_TARGET=linux-x64
    FLUTTER_OUTPUT=x64
    ;;
  aarch64)
    FLUTTER_TARGET=linux-arm64
    FLUTTER_OUTPUT=arm64
    ;;
esac

HOST_ARCH="$(normalize_arch "$(uname -m)")"
BUILD_ARGS=(linux --release --target-platform "$FLUTTER_TARGET")
if [[ "$ARCH" != "$HOST_ARCH" ]]; then
  if [[ -z "${FORGE_FLATPAK_SYSROOT:-}" ]]; then
    echo "Cross-building $ARCH from $HOST_ARCH requires FORGE_FLATPAK_SYSROOT." >&2
    echo "A native $ARCH Linux host is recommended." >&2
    exit 1
  fi
  BUILD_ARGS+=(--target-sysroot "$FORGE_FLATPAK_SYSROOT")
fi

rm -rf "$PREFIX"
cd "$APP_ROOT"
flutter pub get
flutter build "${BUILD_ARGS[@]}"
BUNDLE="$APP_ROOT/build/linux/$FLUTTER_OUTPUT/release/bundle"
if [[ ! -x "$BUNDLE/forge" ]]; then
  echo "Flutter Linux $ARCH bundle was not produced at $BUNDLE" >&2
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

printf '%s\n' "$ARCH" > "$BUILD_ROOT/architecture"
echo "Prepared $ARCH Flatpak files in $PREFIX"

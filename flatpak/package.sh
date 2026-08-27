#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_ROOT="${FLATPAK_BUILD_ROOT:-$ROOT/build/flatpak-linux}"
PAYLOAD="$ROOT/flatpak/payload"
REPO="$BUILD_ROOT/repo"
BUILDER_DIR="$BUILD_ROOT/builder"
BUNDLE="$ROOT/dist/linux/Forge-x86_64.flatpak"

if [[ "$(uname -s)" != Linux ]]; then
  echo "Flatpak packaging must run on Linux." >&2
  exit 1
fi
for command in flatpak flatpak-builder; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done

"$ROOT/flatpak/build-linux.sh"

rm -rf "$PAYLOAD" "$BUILDER_DIR" "$REPO"
mkdir -p "$PAYLOAD" "$(dirname "$BUNDLE")"
cp -a "$BUILD_ROOT/files/." "$PAYLOAD/"
flatpak-builder --force-clean --default-branch=stable --repo="$REPO" "$BUILDER_DIR" \
  "$ROOT/flatpak/com.tingouw.forge.yml"
flatpak build-bundle "$REPO" "$BUNDLE" com.tingouw.forge stable
flatpak build-update-repo "$REPO" --generate-static-deltas
sha256sum "$BUNDLE"
echo "Built $BUNDLE"

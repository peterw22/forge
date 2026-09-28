#!/usr/bin/env bash
# Package the versioned native tarball; nFPM emits DEB/RPM on either host arch.
# Usage: package-linux-app.sh app-v1.2.3-rc01 amd64 [output-directory]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/app-release-version.sh"
parse_app_tag "${1:-}"
export PACKAGE_ARCH="${2:-}"
case "$PACKAGE_ARCH" in amd64|arm64) ;; *) echo "Expected amd64 or arm64" >&2; exit 1 ;; esac
OUT="${3:-$ROOT/dist/linux}"
OUT="$(cd "$OUT" && pwd)"
command -v nfpm >/dev/null || { echo "Install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.45.0" >&2; exit 1; }
name="Forge-$APP_VERSION-linux-$PACKAGE_ARCH"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
tar -xzf "$OUT/$name.tar.gz" -C "$WORK"
export PACKAGE_BUNDLE="$WORK/$name"
export PACKAGE_VERSION="$APP_PACKAGE_VERSION"
export PACKAGE_LAUNCHER="$ROOT/packaging/linux/forge"
for file in forge helpers/pi-go-agent lib/libflutter_linux_gtk.so data/icudtl.dat LICENSE; do
  test -f "$PACKAGE_BUNDLE/$file" || { echo "Missing bundle file: $file" >&2; exit 1; }
done
for format in deb rpm; do
  nfpm package --config "$ROOT/packaging/linux/nfpm.yaml" \
    --packager "$format" --target "$OUT/$name.$format"
done

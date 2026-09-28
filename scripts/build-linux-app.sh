#!/usr/bin/env bash
# Build a native, unsandboxed Flutter desktop bundle with its local Go agent.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP="$ROOT/flutter/pi_go_app"
if [[ "$(uname -s)" != Linux ]]; then
  echo "Build the Linux app on Linux (native host, VM, or CI)." >&2
  exit 1
fi
for tool in flutter go clang cmake ninja pkg-config tar; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
case "$(uname -m)" in
  x86_64) arch=x64; goarch=amd64 ;;
  aarch64|arm64) arch=arm64; goarch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
OUT="${OUT:-$ROOT/dist/linux}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
name="Forge-linux-$goarch"
BUILD_ARGS=(linux --release --target-platform "linux-$arch")
if [[ -n "${APP_RELEASE_TAG:-}" ]]; then
  source "$ROOT/scripts/app-release-version.sh"
  parse_app_tag "$APP_RELEASE_TAG"
  name="Forge-$APP_VERSION-linux-$goarch"
  BUILD_ARGS+=(--build-name "$APP_BUILD_NAME" --build-number "${APP_BUILD_NUMBER:-1}")
fi
cd "$APP"
flutter pub get
flutter build "${BUILD_ARGS[@]}"
mkdir -p "$WORK/$name/helpers"
cp -a "build/linux/$arch/release/bundle/." "$WORK/$name/"
cd "$ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -trimpath -ldflags='-s -w' \
  -o "$WORK/$name/helpers/pi-go-agent" ./cmd/pi-go-agent
cp LICENSE "$WORK/$name/"
mkdir -p "$WORK/$name/share/applications" "$WORK/$name/share/icons/hicolor/512x512/apps"
cp packaging/linux/com.tingouw.forge.desktop "$WORK/$name/share/applications/"
cp "$APP/web/icons/Icon-512.png" "$WORK/$name/share/icons/hicolor/512x512/apps/com.tingouw.forge.png"
cp packaging/linux/install-user.sh "$WORK/$name/"
chmod 0755 "$WORK/$name/install-user.sh"
tar -C "$WORK" -czf "$OUT/$name.tar.gz" "$name"
echo "Built $OUT/$name.tar.gz (native Flutter, no browser or sandbox)"

#!/usr/bin/env bash
# Separate from the maintainer's APNs-enabled build-macos-app.sh. This produces
# a single-architecture, provisioning-free direct-download DMG for CI.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/app-release-version.sh"
parse_app_tag "${1:-}"
ARCH="${2:-}"
case "$ARCH" in amd64) XCODE_ARCH=x86_64 ;; arm64) XCODE_ARCH=arm64 ;; *) echo "Expected amd64 or arm64" >&2; exit 1 ;; esac
[[ "$(uname -s)" == Darwin ]] || { echo "Build DMGs on macOS" >&2; exit 1; }
APP_ROOT="$ROOT/flutter/pi_go_app"
OUT="${OUT:-$ROOT/dist/app-release}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$APP_ROOT"
flutter pub get
flutter build macos --config-only --release --build-name "$APP_BUILD_NAME" \
  --build-number "${APP_BUILD_NUMBER:-1}"
(
  cd macos
  xcodebuild -workspace Runner.xcworkspace -scheme Runner -configuration Release \
    -derivedDataPath ../build/macos CODE_SIGNING_ALLOWED=NO \
    ARCHS="$XCODE_ARCH" ONLY_ACTIVE_ARCH=NO MACOSX_DEPLOYMENT_TARGET=12.0 build
)
APP="$WORK/image/Forge.app"
mkdir -p "$WORK/image"
ditto "$APP_ROOT/build/macos/Build/Products/Release/Forge.app" "$APP"
mkdir -p "$APP/Contents/Helpers"
cd "$ROOT"
CGO_ENABLED=0 GOOS=darwin GOARCH="$ARCH" go build -trimpath -ldflags='-s -w' \
  -o "$APP/Contents/Helpers/pi-go-agent" ./cmd/pi-go-agent
# Assert that neither the app nor its agent was accidentally built for the host
# architecture alone when another architecture was requested.
lipo "$APP/Contents/MacOS/Forge" -verify_arch "$XCODE_ARCH"
lipo "$APP/Contents/Helpers/pi-go-agent" -verify_arch "$XCODE_ARCH"
IDENTITY="${SIGN_IDENTITY:--}"
OPTIONS=(--force --sign "$IDENTITY")
if [[ "$IDENTITY" == '-' ]]; then
  OPTIONS+=(--timestamp=none)
  echo "WARNING: ad-hoc DMG; downloaded apps are blocked by Gatekeeper without Developer ID and notarization." >&2
else
  OPTIONS+=(--options runtime --timestamp)
fi
codesign "${OPTIONS[@]}" "$APP/Contents/Helpers/pi-go-agent"
# Sign nested code inside-out, then the application (never use --deep to sign).
find "$APP/Contents/Frameworks" -depth \
  \( -name '*.framework' -o -name '*.dylib' \) -print0 | while IFS= read -r -d '' item; do
  codesign "${OPTIONS[@]}" "$item"
done
codesign "${OPTIONS[@]}" --entitlements "$ROOT/packaging/macos/Release.entitlements" "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"

NOTARY=()
if [[ -n "${NOTARY_PROFILE:-}" ]]; then
  NOTARY=(--keychain-profile "$NOTARY_PROFILE")
elif [[ -n "${NOTARY_KEY:-}" ]]; then
  NOTARY=(--key "$NOTARY_KEY" --key-id "${NOTARY_KEY_ID:?}" --issuer "${NOTARY_ISSUER_ID:?}")
fi
notarize() {
  local file="$1" result
  result="$(xcrun notarytool submit "$file" "${NOTARY[@]}" --wait --output-format json)"
  if [[ "$(plutil -extract status raw -o - - <<<"$result")" != Accepted ]]; then
    echo "$result" >&2
    xcrun notarytool log "$(plutil -extract id raw -o - - <<<"$result")" "${NOTARY[@]}" >&2 || true
    return 1
  fi
}
if [[ ${#NOTARY[@]} -gt 0 ]]; then
  [[ "$IDENTITY" != '-' ]] || { echo "Notarization requires Developer ID signing" >&2; exit 1; }
  ditto -c -k --keepParent "$APP" "$WORK/notarize.zip"
  notarize "$WORK/notarize.zip"
  xcrun stapler staple "$APP"
  xcrun stapler validate "$APP"
fi
ln -s /Applications "$WORK/image/Applications"
cp "$ROOT/LICENSE" "$WORK/image/LICENSE"
DMG="$OUT/Forge-$APP_VERSION-macos-$ARCH.dmg"
hdiutil create -volname "Forge $APP_VERSION" -srcfolder "$WORK/image" \
  -ov -format UDZO "$DMG"
if [[ "$IDENTITY" != '-' ]]; then
  codesign --force --sign "$IDENTITY" --timestamp "$DMG"
fi
if [[ ${#NOTARY[@]} -gt 0 ]]; then
  notarize "$DMG"
  xcrun stapler staple "$DMG"
  xcrun stapler validate "$DMG"
else
  echo "WARNING: DMG is not notarized. See docs/releasing.md for signing secrets." >&2
fi
hdiutil verify "$DMG"
echo "Built $DMG"

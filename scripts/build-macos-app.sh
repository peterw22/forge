#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_ROOT="$ROOT/flutter/pi_go_app"
SOURCE_APP="$APP_ROOT/build/macos/Build/Products/Release/Forge.app"
DIST="$ROOT/dist/macos"
HELPER_BUILD="$APP_ROOT/build/macos-agent"
HELPER="$HELPER_BUILD/pi-go-agent"

if [[ -z "${DEVELOPER_DIR:-}" && -x /Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild ]]; then
  export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
fi
if ! xcrun --find xcodebuild >/dev/null 2>&1; then
  echo "Full Xcode is required. Install Xcode and run: sudo xcode-select -s /Applications/Xcode.app/Contents/Developer" >&2
  exit 1
fi

mkdir -p "$HELPER_BUILD"
rm -f "$HELPER_BUILD/pi-go-agent-arm64" "$HELPER_BUILD/pi-go-agent-amd64" "$HELPER"
(
  cd "$ROOT"
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o "$HELPER_BUILD/pi-go-agent-arm64" ./cmd/pi-go-agent
  CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$HELPER_BUILD/pi-go-agent-amd64" ./cmd/pi-go-agent
)
lipo -create "$HELPER_BUILD/pi-go-agent-arm64" "$HELPER_BUILD/pi-go-agent-amd64" -output "$HELPER"
chmod 0755 "$HELPER"

# Keep convenient standalone universal binaries current as well as the helper
# embedded later in Forge.app.
ditto "$HELPER" "$ROOT/pi-go-agent"
chmod 0755 "$ROOT/pi-go-agent"

cd "$APP_ROOT"
flutter pub get
# Restricted production APNs entitlements cannot be applied by Flutter's
# build-time ad-hoc signature. Compile unsigned and sign/export below.
flutter build macos --config-only
(
  cd macos
  xcodebuild -workspace Runner.xcworkspace -scheme Runner -configuration Release \
    -derivedDataPath ../build/macos CODE_SIGNING_ALLOWED=NO MACOSX_DEPLOYMENT_TARGET=12.0 build >/dev/null
)

# Executable helpers belong in Contents/Helpers rather than Flutter assets.
# Sign inside-out because adding the helper invalidates the app's build-time
# signature.
mkdir -p "$SOURCE_APP/Contents/Helpers"
ditto "$HELPER" "$SOURCE_APP/Contents/Helpers/pi-go-agent"

# Respect an explicit SIGN_IDENTITY. Otherwise prefer the certificate accepted
# by Gatekeeper for direct distribution, then fall back to an Apple Development
# identity for local/device testing, and finally ad-hoc signing.
find_codesign_identity() {
  local prefix="$1"
  security find-identity -v -p codesigning 2>/dev/null \
    | awk -F '"' -v prefix="$prefix" 'index($2, prefix) == 1 { print $2; exit }'
}

if [[ -z "${SIGN_IDENTITY+x}" || -z "$SIGN_IDENTITY" ]]; then
  SIGN_IDENTITY="$(find_codesign_identity 'Developer ID Application:')"
  SIGN_KIND="developer-id"
  if [[ -z "$SIGN_IDENTITY" ]]; then
    SIGN_IDENTITY="$(find_codesign_identity 'Apple Development:')"
    SIGN_KIND="development"
  fi
  if [[ -z "$SIGN_IDENTITY" ]]; then
    SIGN_IDENTITY="-"
    SIGN_KIND="adhoc"
  fi
else
  case "$SIGN_IDENTITY" in
    "-") SIGN_KIND="adhoc" ;;
    "Developer ID Application:"*) SIGN_KIND="developer-id" ;;
    "Apple Development:"*) SIGN_KIND="development" ;;
    *) SIGN_KIND="custom" ;;
  esac
fi

echo "Signing Forge with: $SIGN_IDENTITY"
case "$SIGN_KIND" in
  developer-id)
    echo "Developer ID signing selected. Notarize separately for public downloads."
    ;;
  development)
    echo "WARNING: Apple Development signing is for local/testing use." >&2
    echo "WARNING: Gatekeeper on another Mac will not accept it as a Developer ID distribution." >&2
    ;;
  adhoc)
    echo "WARNING: No Apple signing identity found; using ad-hoc signing." >&2
    ;;
esac

SIGN_OPTIONS=(--options runtime --timestamp)
if [[ "$SIGN_KIND" == "adhoc" ]]; then
  # Hardened runtime enforces library Team IDs. Ad-hoc signatures have no real
  # team, so enabling it makes Flutter's separately signed frameworks fail at
  # dyld launch. Certificate builds keep hardened runtime and timestamps.
  SIGN_OPTIONS=(--timestamp=none)
fi
codesign --force "${SIGN_OPTIONS[@]}" --sign "$SIGN_IDENTITY" "$SOURCE_APP/Contents/Helpers/pi-go-agent"
# Unsigned Xcode compilation leaves Flutter and plugin frameworks unsigned.
# Sign each nested code object before the extension and parent application.
find "$SOURCE_APP/Contents/Frameworks" -depth \
  \( -name '*.framework' -o -name '*.dylib' \) -print0 | while IFS= read -r -d '' item; do
  codesign --force "${SIGN_OPTIONS[@]}" --sign "$SIGN_IDENTITY" "$item"
done
codesign --force "${SIGN_OPTIONS[@]}" \
  --entitlements "$APP_ROOT/macos/Runner/Release.entitlements" \
  --sign "$SIGN_IDENTITY" "$SOURCE_APP"

if [[ "$SIGN_KIND" == "developer-id" ]]; then
  ARCHIVE="$APP_ROOT/build/macos/Forge.xcarchive"
  EXPORT="$APP_ROOT/build/macos/developer-id-export"
  rm -rf "$ARCHIVE" "$EXPORT"
  (
    cd "$APP_ROOT/macos"
    xcodebuild -workspace Runner.xcworkspace -scheme Runner -configuration Release \
      -destination 'generic/platform=macOS' -archivePath "$ARCHIVE" \
      CODE_SIGNING_ALLOWED=NO archive >/dev/null
  )
  rm -rf "$ARCHIVE/Products/Applications/Forge.app"
  ditto "$SOURCE_APP" "$ARCHIVE/Products/Applications/Forge.app"
  plutil -replace ApplicationProperties.SigningIdentity -string "$SIGN_IDENTITY" "$ARCHIVE/Info.plist"
  plutil -replace ApplicationProperties.Team -string "${TEAM_ID:-QJ6C3M6J85}" "$ARCHIVE/Info.plist"
  xcodebuild -exportArchive -archivePath "$ARCHIVE" -exportPath "$EXPORT" \
    -exportOptionsPlist "$APP_ROOT/macos/DeveloperIDExportOptions.plist" \
    -allowProvisioningUpdates >/dev/null
  rm -rf "$SOURCE_APP"
  ditto "$EXPORT/Forge.app" "$SOURCE_APP"
fi

codesign --verify --deep --strict --verbose=2 "$SOURCE_APP"

mkdir -p "$DIST"
rm -rf "$DIST/Forge.app" "$DIST/Forge-macOS.zip" "$DIST/pi-go-agent"
ditto "$ROOT/pi-go-agent" "$DIST/pi-go-agent"
codesign --force "${SIGN_OPTIONS[@]}" --sign "$SIGN_IDENTITY" "$DIST/pi-go-agent"
ditto "$SOURCE_APP" "$DIST/Forge.app"
ditto -c -k --sequesterRsrc --keepParent "$DIST/Forge.app" "$DIST/Forge-macOS.zip"

lipo -info "$ROOT/pi-go-agent"
lipo -info "$DIST/pi-go-agent"
lipo -info "$DIST/Forge.app/Contents/Helpers/pi-go-agent"
echo "Signature details:"
codesign -dv --verbose=2 "$DIST/Forge.app" 2>&1 \
  | grep -E '^(Identifier|Authority|TeamIdentifier|Signature)=' || true
echo "Signed app entitlements:"
codesign -d --entitlements :- "$DIST/Forge.app" 2>/dev/null | plutil -p -
if [[ "$SIGN_KIND" == "developer-id" ]]; then
  test -f "$DIST/Forge.app/Contents/embedded.provisionprofile"
  security cms -D -i "$DIST/Forge.app/Contents/embedded.provisionprofile" > "$APP_ROOT/build/macos/embedded-profile.plist"
  test "$(/usr/libexec/PlistBuddy -c 'Print :Entitlements:com.apple.developer.aps-environment' "$APP_ROOT/build/macos/embedded-profile.plist")" = production
fi
if [[ "$SIGN_KIND" == "developer-id" ]]; then
  echo "Gatekeeper assessment (notarization may still be required):"
  spctl --assess --type execute --verbose=2 "$DIST/Forge.app" 2>&1 || true
fi
echo "Built $ROOT/pi-go-agent"
echo "Built $DIST/pi-go-agent"
echo "Built $DIST/Forge.app"
echo "Built $DIST/Forge-macOS.zip"

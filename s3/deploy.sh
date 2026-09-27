#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_ROOT="$ROOT/flutter/pi_go_app"
S3_ROOT="$ROOT/s3"
# Signing details of one deployment live in an untracked file; see README.md.
if [[ -f "$S3_ROOT/deploy.local.env" ]]; then
  # shellcheck source=/dev/null
  source "$S3_ROOT/deploy.local.env"
fi
BUCKET="${R2_BUCKET:-forge-app}"
PUBLIC_ORIGIN="${PUBLIC_ORIGIN:-https://forge-app.tingouw.com}"
BUILD_NAME="${BUILD_NAME:-1.0.0}"
BUILD_NUMBER="${BUILD_NUMBER:-13}"
WRANGLER_DIR="$ROOT/worker-push"
TEAM_ID="${TEAM_ID:-QJ6C3M6J85}"
BUNDLE_ID="${BUNDLE_ID:-com.tingouw.forge}"
EXPORT_OPTIONS="${EXPORT_OPTIONS:-$S3_ROOT/AdHocExportOptions.plist}"
# Optional checks of the Ad Hoc profile: how many devices it must hold and
# which UDIDs (separated by spaces or commas) must be among them.
EXPECTED_ADHOC_DEVICE_COUNT="${EXPECTED_ADHOC_DEVICE_COUNT:-}"
REQUIRED_ADHOC_DEVICES="${REQUIRED_ADHOC_DEVICES:-}"

if [[ ! -f "$EXPORT_OPTIONS" ]]; then
  echo "Missing $EXPORT_OPTIONS; copy AdHocExportOptions.example.plist and fill it in" >&2
  exit 1
fi

if [[ -z "${DEVELOPER_DIR:-}" && -x /Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild ]]; then
  export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
fi

cd "$APP_ROOT"
flutter build ipa --release \
  --export-options-plist "$EXPORT_OPTIONS" \
  --build-name "$BUILD_NAME" --build-number "$BUILD_NUMBER"

IPA_SOURCE="$(find build/ios/ipa -maxdepth 1 -name '*.ipa' -print -quit)"
if [[ -z "$IPA_SOURCE" ]]; then
  echo "Ad Hoc IPA was not produced" >&2
  exit 1
fi

mkdir -p "$S3_ROOT/ios"
cp "$IPA_SOURCE" "$S3_ROOT/ios/Forge.ipa"

# Refuse publication unless the Ad Hoc app and encrypted notification service
# extension have valid distribution signatures and the expected entitlements.
VERIFY_DIR="$(mktemp -d)"
trap 'rm -rf "$VERIFY_DIR"' EXIT
unzip -q "$S3_ROOT/ios/Forge.ipa" -d "$VERIFY_DIR"
APP="$VERIFY_DIR/Payload/Runner.app"
EXTENSION="$APP/PlugIns/ForgeNotificationService.appex"
test -d "$EXTENSION"
codesign --verify --deep --strict "$APP"
codesign -d --entitlements :- "$APP" 2>/dev/null > "$VERIFY_DIR/app-entitlements.plist"
codesign -d --entitlements :- "$EXTENSION" 2>/dev/null > "$VERIFY_DIR/extension-entitlements.plist"
test "$(plutil -extract aps-environment raw -o - "$VERIFY_DIR/app-entitlements.plist")" = production
test "$(plutil -extract application-identifier raw -o - "$VERIFY_DIR/app-entitlements.plist")" = "$TEAM_ID.$BUNDLE_ID"
test "$(plutil -extract application-identifier raw -o - "$VERIFY_DIR/extension-entitlements.plist")" = "$TEAM_ID.$BUNDLE_ID.notification-service"
/usr/libexec/PlistBuddy -c 'Print :keychain-access-groups:0' "$VERIFY_DIR/app-entitlements.plist"   | grep -qx "$TEAM_ID.$BUNDLE_ID.push-content"
/usr/libexec/PlistBuddy -c 'Print :keychain-access-groups:0' "$VERIFY_DIR/extension-entitlements.plist"   | grep -qx "$TEAM_ID.$BUNDLE_ID.push-content"
test "$(plutil -extract CFBundleVersion raw -o - "$APP/Info.plist")" = "$BUILD_NUMBER"
test "$(plutil -extract CFBundleVersion raw -o - "$EXTENSION/Info.plist")" = "$BUILD_NUMBER"
security cms -D -i "$APP/embedded.mobileprovision" > "$VERIFY_DIR/app-profile.plist"
security cms -D -i "$EXTENSION/embedded.mobileprovision" > "$VERIFY_DIR/extension-profile.plist"
python3 - "$VERIFY_DIR/app-profile.plist" "$VERIFY_DIR/extension-profile.plist" "$EXPECTED_ADHOC_DEVICE_COUNT" "$REQUIRED_ADHOC_DEVICES" <<'PYPROFILE'
import plistlib, sys
app_path, extension_path, expected_text, required_text = sys.argv[1:]
with open(app_path, 'rb') as handle:
    app_devices = set(plistlib.load(handle).get('ProvisionedDevices', []))
with open(extension_path, 'rb') as handle:
    extension_devices = set(plistlib.load(handle).get('ProvisionedDevices', []))
if app_devices != extension_devices:
    raise SystemExit(f'app and extension Ad Hoc device sets differ: {sorted(app_devices)} / {sorted(extension_devices)}')
missing = sorted(set(required_text.replace(',', ' ').split()) - app_devices)
if missing:
    raise SystemExit(f'required devices are absent from the Ad Hoc profile: {missing}')
if expected_text and len(app_devices) != int(expected_text):
    raise SystemExit(f'expected {expected_text} Ad Hoc devices, found {len(app_devices)}: {sorted(app_devices)}')
print(f'Verified {len(app_devices)} Ad Hoc devices: {sorted(app_devices)}')
PYPROFILE

python3 - "$S3_ROOT/manifest.plist" "$PUBLIC_ORIGIN" "$BUILD_NUMBER" <<'PY'
import plistlib, sys
path, origin, build = sys.argv[1:]
with open(path, 'rb') as handle:
    manifest = plistlib.load(handle)
item = manifest['items'][0]
# Versioned URLs keep iOS and the CDN from installing a cached older build.
item['assets'][0]['url'] = origin.rstrip('/') + '/ios/Forge.ipa?v=' + build
item['metadata']['bundle-version'] = build
with open(path, 'wb') as handle:
    plistlib.dump(manifest, handle, fmt=plistlib.FMT_XML, sort_keys=False)
PY

python3 - "$S3_ROOT/index.html" "$PUBLIC_ORIGIN" "$BUILD_NAME" "$BUILD_NUMBER" <<'PY'
from pathlib import Path
from urllib.parse import quote
import re, sys
path, origin, version, build = sys.argv[1:]
p = Path(path)
text = p.read_text()
manifest = origin.rstrip('/') + '/ios/manifest.plist?v=' + build
link = 'itms-services://?action=download-manifest&url=' + quote(manifest, safe='')
text = re.sub(r'itms-services://[^\"]+', link.replace('&', '&amp;'), text)
text = re.sub(r'Version [^<]+', f'Version {version} (Build {build})', text, count=1)
p.write_text(text)
PY

cd "$WRANGLER_DIR"
npx wrangler r2 object put "$BUCKET/ios/Forge.ipa" \
  --file "$S3_ROOT/ios/Forge.ipa" \
  --content-type "application/octet-stream" --remote
npx wrangler r2 object put "$BUCKET/ios/manifest.plist" \
  --file "$S3_ROOT/manifest.plist" \
  --content-type "application/xml; charset=utf-8" --remote
npx wrangler r2 object put "$BUCKET/index.html" \
  --file "$S3_ROOT/index.html" \
  --content-type "text/html; charset=utf-8" --remote
# R2 custom domains do not automatically resolve index.html. An empty object
# key serves the domain root; 404.html is also useful if website mode is enabled.
cp "$S3_ROOT/index.html" "$S3_ROOT/404.html"
npx wrangler r2 object put "$BUCKET/" \
  --file "$S3_ROOT/index.html" \
  --content-type "text/html; charset=utf-8" --remote
npx wrangler r2 object put "$BUCKET/404.html" \
  --file "$S3_ROOT/404.html" \
  --content-type "text/html; charset=utf-8" --remote

shasum -a 256 "$S3_ROOT/ios/Forge.ipa"
echo "Published $PUBLIC_ORIGIN"

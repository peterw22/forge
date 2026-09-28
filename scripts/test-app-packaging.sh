#!/usr/bin/env bash
# Fast checks require only Bash. If nfpm is installed, build real fixture DEBs
# and RPMs for both architectures without needing a Flutter/Linux build host.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/app-release-version.sh"
for tag in app-v1.2.3 app-v1.2.3-rc01 app-v0.0.0-rc0 app-v10.20.30-beta.2 app-v1.2.3-anything app-v1.2.3-nightly-2026.09.27; do
  parse_app_tag "$tag"
  test "$APP_VERSION" = "${tag#app-v}"
done
parse_app_tag app-v1.2.3
test "$APP_BUILD_NAME" = 1.2.3
test "$APP_PACKAGE_VERSION" = 1.2.3
test "$APP_PRERELEASE" = false
parse_app_tag app-v1.2.3-rc09
test "$APP_BUILD_NAME" = 1.2.3
test "$APP_PACKAGE_VERSION" = '1.2.3~rc09'
test "$APP_PRERELEASE" = true
parse_app_tag app-v1.2.3-nightly-2026.09.27
test "$APP_PACKAGE_VERSION" = '1.2.3~nightly.2026.09.27'
for tag in '' v1.2.3 v1.2.3-rc01 app-v1.2 app-v1.2.3- app-v01.2.3 app-v1.2.3-rc/01 'app-v1.2.3-rc01;echo bad'; do
  if parse_app_tag "$tag" 2>/dev/null; then
    echo "Accepted invalid tag: $tag" >&2
    exit 1
  fi
done
# Test the actual workflow tag filters, not only the version parser. GitHub's
# simple prefix globs used here have the same matching behavior as Bash.
agent_filter="$(awk -F "'" '/tags: / { print $2; exit }' "$ROOT/.github/workflows/release.yml")"
app_filter="$(awk -F "'" '/tags: / { print $2; exit }' "$ROOT/.github/workflows/app-release.yml")"
test "$agent_filter" = 'v*'
test "$app_filter" = 'app-v*'
for tag in app-v1.2.3 app-v1.2.3-anything; do
  [[ "$tag" == $app_filter && "$tag" != $agent_filter ]]
done
for tag in v1.2.3 v1.2.3-anything; do
  [[ "$tag" == $agent_filter && "$tag" != $app_filter ]]
done

for script in app-release-version build-linux-app package-linux-app verify-linux-packages build-macos-dmg prepare-app-signing; do
  bash -n "$ROOT/scripts/$script.sh"
done
if ! command -v nfpm >/dev/null; then
  echo 'Tag and syntax tests passed; install nfpm to run package fixture tests.'
  exit 0
fi
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for arch in amd64 arm64; do
  name="Forge-1.2.3-rc09-linux-$arch"
  bundle="$WORK/$name"
  mkdir -p "$bundle/helpers" "$bundle/lib" "$bundle/data/flutter_assets" \
    "$bundle/share/applications" "$bundle/share/icons/hicolor/512x512/apps"
  # Dummy payloads validate metadata and directory recursion, not execution.
  cp /usr/bin/true "$bundle/forge"
  cp /usr/bin/true "$bundle/helpers/pi-go-agent"
  touch "$bundle/lib/libflutter_linux_gtk.so" "$bundle/data/icudtl.dat" "$bundle/data/flutter_assets/test-asset"
  cp "$ROOT/LICENSE" "$bundle/"
  cp "$ROOT/packaging/linux/com.tingouw.forge.desktop" "$bundle/share/applications/"
  cp "$ROOT/flutter/pi_go_app/web/icons/Icon-512.png" "$bundle/share/icons/hicolor/512x512/apps/com.tingouw.forge.png"
  tar -C "$WORK" -czf "$WORK/$name.tar.gz" "$name"
  "$ROOT/scripts/package-linux-app.sh" app-v1.2.3-rc09 "$arch" "$WORK"
  test -s "$WORK/$name.deb"
  test -s "$WORK/$name.rpm"
  # ar + tar are also available on macOS, so test payload layout locally too.
  mkdir -p "$WORK/payload-$arch"
  ar -p "$WORK/$name.deb" data.tar.gz | tar -xz -C "$WORK/payload-$arch"
  test -f "$WORK/payload-$arch/usr/lib/forge/data/flutter_assets/test-asset"
  test -f "$WORK/payload-$arch/usr/lib/forge/lib/libflutter_linux_gtk.so"
  test -x "$WORK/payload-$arch/usr/lib/forge/helpers/pi-go-agent"
  test -x "$WORK/payload-$arch/usr/bin/forge"
  cmp "$ROOT/packaging/linux/forge" "$WORK/payload-$arch/usr/bin/forge"
  mkdir -p "$WORK/control-$arch"
  ar -p "$WORK/$name.deb" control.tar.gz | tar -xz -C "$WORK/control-$arch"
  grep -qx "Architecture: $arch" "$WORK/control-$arch/control"
  grep -qx 'Version: 1.2.3~rc09-1' "$WORK/control-$arch/control"
  if command -v bsdtar >/dev/null; then
    mkdir -p "$WORK/rpm-bsdtar-$arch"
    bsdtar -xf "$WORK/$name.rpm" -C "$WORK/rpm-bsdtar-$arch"
    test -f "$WORK/rpm-bsdtar-$arch/usr/lib/forge/data/flutter_assets/test-asset"
    test -x "$WORK/rpm-bsdtar-$arch/usr/lib/forge/helpers/pi-go-agent"
    test -x "$WORK/rpm-bsdtar-$arch/usr/bin/forge"
    cmp "$ROOT/packaging/linux/forge" "$WORK/rpm-bsdtar-$arch/usr/bin/forge"
  fi
  if command -v rpm2cpio >/dev/null; then
    rpm_file="$WORK/$name.rpm"
    mkdir -p "$WORK/rpm-payload-$arch"
    (cd "$WORK/rpm-payload-$arch" && rpm2cpio "$rpm_file" | cpio -idm --quiet --no-absolute-filenames)
    test -f "$WORK/rpm-payload-$arch/usr/lib/forge/data/flutter_assets/test-asset"
    test -x "$WORK/rpm-payload-$arch/usr/lib/forge/helpers/pi-go-agent"
    test -x "$WORK/rpm-payload-$arch/usr/bin/forge"
    cmp "$ROOT/packaging/linux/forge" "$WORK/rpm-payload-$arch/usr/bin/forge"
  fi
  if command -v dpkg-deb >/dev/null; then
    test "$(dpkg-deb -f "$WORK/$name.deb" Architecture)" = "$arch"
    test "$(dpkg-deb -f "$WORK/$name.deb" Version)" = '1.2.3~rc09-1'
    dpkg-deb -x "$WORK/$name.deb" "$WORK/extracted-$arch"
    test -f "$WORK/extracted-$arch/usr/lib/forge/data/flutter_assets/test-asset"
    test -x "$WORK/extracted-$arch/usr/lib/forge/helpers/pi-go-agent"
    test -x "$WORK/extracted-$arch/usr/bin/forge"
    cmp "$ROOT/packaging/linux/forge" "$WORK/extracted-$arch/usr/bin/forge"
  fi
done
echo 'Tag, script syntax and both-architecture DEB/RPM fixture tests passed.'

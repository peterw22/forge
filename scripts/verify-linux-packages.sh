#!/usr/bin/env bash
# Inspect package metadata/payloads and check bundled dynamic-library resolution.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/app-release-version.sh"
parse_app_tag "${1:-}"
arch="${2:-}"
case "$arch" in amd64) rpm_arch=x86_64 ;; arm64) rpm_arch=aarch64 ;; *) exit 1 ;; esac
OUT="${3:-$ROOT/dist/app-release}"
name="Forge-$APP_VERSION-linux-$arch"
deb="$OUT/$name.deb"
rpm="$OUT/$name.rpm"
deb="$(cd "$(dirname "$deb")" && pwd)/$(basename "$deb")"
rpm="$(cd "$(dirname "$rpm")" && pwd)/$(basename "$rpm")"
test "$(dpkg-deb -f "$deb" Architecture)" = "$arch"
test "$(dpkg-deb -f "$deb" Version)" = "$APP_PACKAGE_VERSION-1"
test "$(rpm -qp --qf '%{ARCH}' "$rpm")" = "$rpm_arch"
test "$(rpm -qp --qf '%{VERSION}-%{RELEASE}' "$rpm")" = "$APP_PACKAGE_VERSION-1"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
dpkg-deb -x "$deb" "$WORK/deb"
mkdir "$WORK/rpm"
(cd "$WORK/rpm" && rpm2cpio "$rpm" | cpio -idm --quiet)
for format in deb rpm; do
  root="$WORK/$format"
  test -x "$root/usr/lib/forge/forge"
  test -x "$root/usr/lib/forge/helpers/pi-go-agent"
  test -f "$root/usr/lib/forge/lib/libflutter_linux_gtk.so"
  test -f "$root/usr/lib/forge/data/icudtl.dat"
  test -f "$root/usr/share/applications/com.tingouw.forge.desktop"
  test "$(readlink "$root/usr/bin/forge")" = /usr/lib/forge/forge
  # Check every shipped library, not only the launcher. Missing dependencies
  # fail the release rather than shipping a package that cannot load plugins.
  while IFS= read -r -d '' binary; do
    dependencies="$(LD_LIBRARY_PATH="$root/usr/lib/forge/lib" ldd "$binary")"
    if [[ "$dependencies" == *'not found'* ]]; then
      echo "$binary: $dependencies" >&2
      exit 1
    fi
  done < <(find "$root/usr/lib/forge" -type f \( -name forge -o -name '*.so' \) -print0)
done
echo "Verified $arch DEB and RPM payloads and runtime dependencies"

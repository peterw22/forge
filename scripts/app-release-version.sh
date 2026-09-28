#!/usr/bin/env bash
# Source and call parse_app_tag, or execute to emit GitHub Actions outputs.
# Mirrors agent v* releases with an app- prefix. A suffix means prerelease.
parse_app_tag() {
  local tag="${1:-}" suffix
  if [[ ! "$tag" =~ ^app-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([[:alnum:]][[:alnum:].-]*))?$ ]]; then
    echo "Expected app-vX.X.X or app-vX.X.X-<suffix>, e.g. app-v1.2.3 or app-v1.2.3-rc01" >&2
    return 1
  fi
  APP_VERSION="${tag#app-v}"
  APP_BUILD_NAME="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
  suffix="${BASH_REMATCH[5]:-}"
  APP_PRERELEASE=false
  APP_PACKAGE_VERSION="$APP_BUILD_NAME"
  if [[ -n "$suffix" ]]; then
    APP_PRERELEASE=true
    # Tilde sorts before the final version in both dpkg and modern RPM.
    # RPM Version cannot contain '-'; normalize suffix separators to dots.
    APP_PACKAGE_VERSION="$APP_BUILD_NAME~${suffix//-/.}"
  fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -euo pipefail
  parse_app_tag "${1:-}"
  printf 'version=%s\nbuild_name=%s\npackage_version=%s\nprerelease=%s\n' \
    "$APP_VERSION" "$APP_BUILD_NAME" "$APP_PACKAGE_VERSION" "$APP_PRERELEASE"
fi

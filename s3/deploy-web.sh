#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_ROOT="$ROOT/flutter/pi_go_app"
BUCKET="${R2_BUCKET:-forge-web}"
PUBLIC_ORIGIN="${PUBLIC_ORIGIN:-https://forge.tingouw.com}"
# Optional key prefix (for example "web") to publish under a subpath.
WEB_PREFIX="${WEB_PREFIX:-}"
KEY_PREFIX="${WEB_PREFIX:+$WEB_PREFIX/}"
BUILD_NAME="${BUILD_NAME:-1.0.1}"
BUILD_NUMBER="${BUILD_NUMBER:-17}"
WRANGLER_DIR="$ROOT/worker-push"

cd "$APP_ROOT"
# --wasm adds a dart2wasm/skwasm build that browsers with WasmGC load first;
# the dart2js/CanvasKit build is still produced as the fallback.
flutter build web --wasm --release --base-href "/$KEY_PREFIX" \
  --build-name "$BUILD_NAME" --build-number "$BUILD_NUMBER"
WEB_ROOT="$APP_ROOT/build/web"

# Refuse publication if the service worker precaches a file the build lacks;
# cache.addAll() would otherwise fail and the PWA would never install.
grep -o "'\./[^']*'" "$WEB_ROOT/forge_service_worker.js" | tr -d "'" | sed 's/?.*//' |
  while read -r entry; do
    [[ "$entry" == ./ ]] && continue
    test -f "$WEB_ROOT/$entry" || { echo "service worker precaches missing $entry" >&2; exit 1; }
  done

content_type() {
  case "$1" in
    *.html) echo "text/html; charset=utf-8" ;;
    # Browsers refuse module scripts (main.dart.mjs) with any non-JavaScript type.
    *.js|*.mjs) echo "text/javascript; charset=utf-8" ;;
    *.json) echo "application/json; charset=utf-8" ;;
    *.wasm) echo "application/wasm" ;;
    *.png) echo "image/png" ;;
    *.otf) echo "font/otf" ;;
    *.ttf) echo "font/ttf" ;;
    *) echo "application/octet-stream" ;;
  esac
}

# Entry points are revalidated on every load so a release is picked up
# immediately; the remaining assets change only with the Flutter engine.
cache_control() {
  case "$1" in
    index.html|flutter_bootstrap.js|forge_service_worker.js|flutter_service_worker.js|main.dart.js|main.dart.mjs|main.dart.wasm|version.json|manifest.json)
      echo "no-cache" ;;
    *) echo "public, max-age=3600" ;;
  esac
}

put() {
  local key="$1" file="$2" rel="$3"
  npx wrangler r2 object put "$BUCKET/$key" --file "$file" \
    --content-type "$(content_type "$rel")" \
    --cache-control "$(cache_control "$rel")" --remote
}

cd "$WRANGLER_DIR"
# Upload index.html last so clients never load a shell that references
# assets not yet published.
(cd "$WEB_ROOT" && find . -type f ! -name '*.symbols' ! -name '.last_build_id' ! -path './index.html' | sed 's|^\./||' | sort) |
  while read -r rel; do
    put "$KEY_PREFIX$rel" "$WEB_ROOT/$rel" "$rel"
  done
put "${KEY_PREFIX}index.html" "$WEB_ROOT/index.html" index.html
# R2 custom domains do not resolve directory indexes, so publish the shell at
# the directory key as well (an empty key serves the domain root).
put "$KEY_PREFIX" "$WEB_ROOT/index.html" index.html

if [[ -n "$WEB_PREFIX" ]]; then
  # Send bare /prefix to /prefix/ so relative asset URLs resolve under it.
  REDIRECT="$(mktemp)"
  trap 'rm -f "$REDIRECT"' EXIT
  printf '<!DOCTYPE html><meta http-equiv="refresh" content="0; url=/%s"><a href="/%s">Forge</a>\n' \
    "$KEY_PREFIX" "$KEY_PREFIX" > "$REDIRECT"
  put "$WEB_PREFIX" "$REDIRECT" index.html
fi

echo "Published ${PUBLIC_ORIGIN%/}/$KEY_PREFIX"

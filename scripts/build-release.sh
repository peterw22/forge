#!/usr/bin/env bash
# Builds the release archives of pi-go-agent for the system this runs on:
# amd64 and arm64 archives on Linux, one signed universal archive on macOS.
#
#   scripts/build-release.sh v1.2.3
#
# macOS signing, from the environment:
#   SIGN_IDENTITY      "Developer ID Application: ..." (default "-", ad hoc)
#   SIGN_KEYCHAIN      keychain that holds the identity (default: search list)
#   SIGN_IDENTIFIER    code identifier (default com.tingouw.forge.agent)
# macOS notarization, either of:
#   NOTARY_PROFILE     a profile saved with `notarytool store-credentials`
#   NOTARY_KEY, NOTARY_KEY_ID, NOTARY_ISSUER_ID
#                      an App Store Connect API key file, its ID and issuer
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:?usage: build-release.sh <version>}"
OUT="${OUT:-$ROOT/dist/release}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/forge-release.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

build() {
  local os="$1" arch="$2" output="$3"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags='-s -w' -o "$output" ./cmd/pi-go-agent
  )
}

# stage <name> <binary> prints a directory holding the files of one archive.
stage() {
  local name="$1" binary="$2"
  mkdir -p "$WORK/$name"
  cp "$binary" "$WORK/$name/pi-go-agent"
  chmod 0755 "$WORK/$name/pi-go-agent"
  cp "$ROOT/LICENSE" "$WORK/$name/LICENSE"
  echo "$WORK/$name"
}

archive() {
  local name="$1"
  tar -C "$WORK" -czf "$OUT/$name.tar.gz" "$name"
  echo "Built $OUT/$name.tar.gz"
}

sign() {
  local binary="$1" identity="${SIGN_IDENTITY:--}"
  if [[ "$identity" == "-" ]]; then
    echo "WARNING: signing ad hoc. Gatekeeper blocks this build when it is downloaded with a browser." >&2
    codesign --force --sign - "$binary"
  else
    # The notary service accepts only a hardened runtime and a secure timestamp.
    codesign --force --options runtime --timestamp \
      --identifier "${SIGN_IDENTIFIER:-com.tingouw.forge.agent}" \
      ${SIGN_KEYCHAIN:+--keychain "$SIGN_KEYCHAIN"} \
      --sign "$identity" "$binary"
  fi
  codesign --verify --strict --verbose=2 "$binary"
}

notarize() {
  local directory="$1" credentials=()
  if [[ -n "${NOTARY_PROFILE:-}" ]]; then
    credentials=(--keychain-profile "$NOTARY_PROFILE")
  elif [[ -n "${NOTARY_KEY:-}" ]]; then
    credentials=(--key "$NOTARY_KEY" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID")
  else
    echo "WARNING: not notarized. Gatekeeper blocks this build when it is downloaded with a browser." >&2
    return
  fi
  if [[ "${SIGN_IDENTITY:--}" == "-" ]]; then
    echo "Notarization needs a Developer ID signature. Set SIGN_IDENTITY." >&2
    exit 1
  fi
  # The notary service takes no bare executable, and none can carry a stapled
  # ticket. Gatekeeper fetches the ticket by the signature's hash instead.
  ditto -c -k --keepParent "$directory" "$WORK/notarize.zip"
  local result id status
  result="$(xcrun notarytool submit "$WORK/notarize.zip" "${credentials[@]}" --wait --output-format json)"
  id="$(plutil -extract id raw -o - - <<<"$result")"
  status="$(plutil -extract status raw -o - - <<<"$result")"
  echo "Notarization $id: $status"
  if [[ "$status" != "Accepted" ]]; then
    xcrun notarytool log "$id" "${credentials[@]}" >&2 || true
    exit 1
  fi
}

mkdir -p "$OUT"
case "$(uname -s)" in
  Linux)
    for arch in amd64 arm64; do
      name="pi-go-agent-$VERSION-linux-$arch"
      build linux "$arch" "$WORK/$arch"
      stage "$name" "$WORK/$arch" >/dev/null
      archive "$name"
    done
    ;;
  Darwin)
    name="pi-go-agent-$VERSION-macos-universal"
    build darwin arm64 "$WORK/arm64"
    build darwin amd64 "$WORK/amd64"
    lipo -create "$WORK/arm64" "$WORK/amd64" -output "$WORK/universal"
    directory="$(stage "$name" "$WORK/universal")"
    sign "$directory/pi-go-agent"
    notarize "$directory"
    archive "$name"
    ;;
  *)
    echo "No release is built on $(uname -s)." >&2
    exit 1
    ;;
esac

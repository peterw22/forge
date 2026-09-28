#!/usr/bin/env bash
# Import optional release credentials into a temporary CI-only keychain.
# Never print certificate/key contents. Cleanup runs in an always() workflow step.
set -euo pipefail
: "${RUNNER_TEMP:?}" "${GITHUB_ENV:?}"
if [[ -z "${MACOS_CERTIFICATE_P12:-}" ]]; then
  if [[ -n "${NOTARY_KEY_P8:-}" ]]; then
    echo "Notarization credentials require MACOS_CERTIFICATE_P12" >&2
    exit 1
  fi
  echo 'SIGN_IDENTITY=-' >> "$GITHUB_ENV"
  echo '::warning::No Developer ID certificate: DMGs will be ad-hoc signed and not notarized.'
  exit 0
fi
KEYCHAIN="$RUNNER_TEMP/app-release.keychain-db"
password="$(openssl rand -hex 24)"
echo "::add-mask::$password"
umask 077
security create-keychain -p "$password" "$KEYCHAIN"
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$password" "$KEYCHAIN"
base64 --decode <<<"$MACOS_CERTIFICATE_P12" > "$RUNNER_TEMP/app-certificate.p12"
security import "$RUNNER_TEMP/app-certificate.p12" -P "${MACOS_CERTIFICATE_PASSWORD:-}" \
  -f pkcs12 -k "$KEYCHAIN" -T /usr/bin/codesign
rm "$RUNNER_TEMP/app-certificate.p12"
security list-keychains -d user -s "$KEYCHAIN" login.keychain-db
security set-key-partition-list -S apple-tool:,apple: -s -k "$password" "$KEYCHAIN" >/dev/null
identity="$(security find-identity -v -p codesigning "$KEYCHAIN" | awk -F '"' '/Developer ID Application:/ { print $2; exit }')"
[[ -n "$identity" ]] || { echo 'No valid Developer ID Application identity found' >&2; exit 1; }
echo "SIGN_IDENTITY=$identity" >> "$GITHUB_ENV"
if [[ -n "${NOTARY_KEY_P8:-}" ]]; then
  : "${NOTARY_KEY_ID:?}" "${NOTARY_ISSUER_ID:?}"
  base64 --decode <<<"$NOTARY_KEY_P8" > "$RUNNER_TEMP/app-notary.p8"
  echo "NOTARY_KEY=$RUNNER_TEMP/app-notary.p8" >> "$GITHUB_ENV"
else
  echo '::warning::Developer ID signing enabled, but no notarization credentials were supplied.'
fi

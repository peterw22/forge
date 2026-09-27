# Forge Ad Hoc iOS distribution

This directory contains the static installation website and the Ad Hoc IPA uploaded to Cloudflare R2 bucket `forge-app`, served at `https://forge-app.tingouw.com`.

Files:

- `index.html` — Safari installation page
- `manifest.plist` — Apple OTA installation manifest
- `404.html` — root/fallback copy generated from `index.html`
- `ios/Forge.ipa` — generated Ad Hoc IPA (created by deployment)
- `deploy.sh` — builds, verifies, and uploads all objects with Wrangler

Deploy version 1.0.0 build 3:

```bash
./s3/deploy.sh
```

Override values when publishing another build:

```bash
BUILD_NAME=1.0.0 BUILD_NUMBER=4 \
PUBLIC_ORIGIN=https://forge-app.tingouw.com \
R2_BUCKET=forge-app ./s3/deploy.sh
```

Before building, register every target iPhone UDID in Apple Developer. Xcode automatically creates/downloads an Ad Hoc profile containing currently registered devices. Adding another device requires rebuilding/re-exporting the IPA with a refreshed profile.

Verify the public endpoints:

```bash
curl -I https://forge-app.tingouw.com/
curl -I https://forge-app.tingouw.com/ios/manifest.plist
curl -I https://forge-app.tingouw.com/ios/Forge.ipa
```

## Web client

`deploy-web.sh` builds the Flutter web app and uploads it to the root of the separate R2 bucket `forge-web`, served at `https://forge.tingouw.com`:

```bash
./s3/deploy-web.sh
BUILD_NAME=1.0.1 BUILD_NUMBER=18 ./s3/deploy-web.sh
```

Set `WEB_PREFIX=web` to publish under a subpath instead (base href `/web/`).

The build uses `--wasm`: browsers with WasmGC load `main.dart.wasm` with the skwasm renderer, and others fall back to `main.dart.js` with CanvasKit. `main.dart.mjs` must be served as JavaScript or the WebAssembly build fails to start, and there's no fallback in that case. Without `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Embedder-Policy: require-corp` headers, skwasm runs single-threaded. If those headers are added (for example with a Cloudflare response header rule), the CSP's `worker-src 'self' blob:` lets its worker start.

Bump the `?v=` cache version in `web/index.html`, `web/flutter_bootstrap.js`, and `web/forge_service_worker.js` for each release so installed PWAs pick up the new build.

The IPA uses production APNs. Anyone may download a public IPA, but iOS installs it only on UDIDs embedded in the Ad Hoc provisioning profile.

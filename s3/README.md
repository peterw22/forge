# Publishing builds

Scripts that publish the iOS app and the web client to Cloudflare R2. They are the maintainer's release process; the addresses and buckets below are the maintainer's. To publish your own builds, see [running your own deployment](../docs/deployment.md).

| Script | Publishes | To |
|---|---|---|
| `deploy.sh` | An Ad Hoc build of the iOS app and an installation page | Bucket `forge-app`, served at `https://forge-app.tingouw.com` |
| `deploy-web.sh` | The web client | Bucket `forge-web`, served at `https://forge.tingouw.com` |

Both upload with Wrangler, using the login in `worker-push/`.

## iOS

```bash
BUILD_NAME=1.0.1 BUILD_NUMBER=21 ./s3/deploy.sh
```

Use a new build number for each release. The manifest and the installation link carry it, so that neither iOS nor the network serves a cached older build.

| File | Purpose |
|---|---|
| `index.html`, `404.html` | The installation page |
| `manifest.plist` | The manifest iOS reads to install the app |
| `ios/Forge.ipa` | The build, created by the script and ignored by Git |

An Ad Hoc build installs only on devices registered in its provisioning profile. Register a device in your Apple developer account, then build again.

### Before it uploads

The script refuses to publish unless:

- the app and its notification extension have valid signatures;
- the push environment is production;
- the app and the extension have the expected identifiers and share the keychain group for notification keys;
- both carry the build number that was asked for;
- the app and the extension are provisioned for the same devices.

### Local configuration

Signing details stay out of the repository. Create these two files in `s3/`; both are ignored by Git.

`AdHocExportOptions.plist`: copy `AdHocExportOptions.example.plist` and fill in your team and the names of your provisioning profiles.

`deploy.local.env`, optional:

```bash
# How many devices the profile must contain
EXPECTED_ADHOC_DEVICE_COUNT=2
# Devices that must be among them, separated by spaces or commas
REQUIRED_ADHOC_DEVICES="<UDID> <UDID>"
```

### Settings

| Variable | Default |
|---|---|
| `BUILD_NAME`, `BUILD_NUMBER` | `1.0.0`, `13` |
| `R2_BUCKET` | `forge-app` |
| `PUBLIC_ORIGIN` | `https://forge-app.tingouw.com` |
| `TEAM_ID`, `BUNDLE_ID` | The maintainer's |
| `EXPORT_OPTIONS` | `s3/AdHocExportOptions.plist` |

### Check

```bash
curl -I https://forge-app.tingouw.com/
curl -I https://forge-app.tingouw.com/ios/manifest.plist
curl -I https://forge-app.tingouw.com/ios/Forge.ipa
```

## Web

```bash
BUILD_NAME=1.0.1 BUILD_NUMBER=21 ./s3/deploy-web.sh
```

`WEB_PREFIX=web` publishes under `/web/`.

### Before each release

Raise the cache version in these three files, so that installed copies pick up the new build:

- `flutter/pi_go_app/web/index.html`
- `flutter/pi_go_app/web/flutter_bootstrap.js`
- `flutter/pi_go_app/web/forge_service_worker.js`

### How it is served

- Browsers that support it load the WebAssembly build; others fall back to JavaScript.
- `main.dart.mjs` must be served as JavaScript. Otherwise the WebAssembly build fails to start, and there is no fallback in that case. The script sets the content type.
- Entry files are revalidated on every load. Other files are cached for an hour.
- The page is uploaded last, so that it never refers to a file that is not there yet.
- The script refuses to publish if the service worker lists a file the build does not contain.

The WebAssembly build runs on one thread unless the site sends `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Embedder-Policy: require-corp`.

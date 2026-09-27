# Pi Go Flutter client

Native Flutter clients for the `pi-go-agent` protocol. The macOS application is **Forge** (`com.tingouw.forge`) and is a WebSocket-only client: it does not bundle, launch, or manage a Go server.

Start the agent separately:

```bash
cd go
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd /path/to/project
```

Then run the native macOS app:

```bash
cd flutter/pi_go_app
flutter pub get
flutter run -d macos
```

Build a distributable application and zip from the repository's `go/` directory:

```bash
./scripts/build-macos-app.sh
# dist/macos/Forge.app
# dist/macos/Forge-macOS.zip
```

Full Xcode is required for macOS builds. The build script auto-discovers `Developer ID Application` first, then `Apple Development`, then ad-hoc signing. `SIGN_IDENTITY` overrides this selection. `Apple Development: Tingou Wu (KUB5DTDMMS)` is available on the current development Mac, but Apple Development certificates are for local/testing builds and do not make browser-downloaded apps pass Gatekeeper on other Macs. Direct public distribution needs Developer ID Application signing plus notarization. Gatekeeper is the macOS service that assesses quarantined downloaded software; check it with `spctl --assess --type execute --verbose=2 dist/macos/Forge.app`.

For browser use, start the WebSocket endpoint and run Flutter Web:

```bash
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd /path/to/project
flutter run -d chrome
# or: flutter build web --release
```

## iOS / TestFlight

Debug and Profile use `Runner/Runner.entitlements` with sandbox APNs. Release uses `Runner/RunnerRelease.entitlements` requesting production APNs for TestFlight/App Store distribution. A Release build installed directly by Xcode may still be re-signed with a development provisioning profile and receive a sandbox token; App Store Connect distribution replaces it with the production entitlement/profile.

Every upload needs a unique build number. Build an App Store archive with full Xcode selected:

```bash
cd flutter/pi_go_app
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer \
  flutter build ipa --release --export-method app-store
```

Upload `build/ios/ipa/*.ipa` through Xcode Organizer or Transporter. Bundle ID: `com.tingouw.forge`; Apple team: `QJ6C3M6J85`.

## Android

The Android application ID is `com.tingouw.forge` and the launcher name is **Forge**.

```bash
flutter build apk --release
adb reverse tcp:7346 tcp:7346
adb install -r build/app/outputs/flutter-apk/app-release.apk
```

With `adb reverse` active, the Android app's default `ws://127.0.0.1:7346/ws` address reaches the agent running on the development machine. Use a trusted WSS endpoint instead when the device is not connected over ADB.

The macOS app sandbox is enabled with outbound-network and user-selected read-only file access. The app communicates only with the configured WebSocket backend. Its attached session ID is retained across connection drops, so reconnecting reattaches to the same detached run. Multiple Forge clients may observe one shared session, or select separate sessions that execute concurrently in the daemon. TCP and WebSocket listeners remain restricted to loopback by the Go backend unless `--allow-remote` is explicitly supplied.

The **YOLO** switch immediately left of Sessions is session-specific. While enabled, `pi-go-agent` skips the Luna tool-safety classifier and manual approval gate. It can only be changed while the session is idle and is restored when that session is reopened.

## Platform look

Forge follows the platform it runs on; on the web this is the platform of the browser's operating system. `lib/forge_theme.dart` builds the theme and `lib/forge_adaptive.dart` presents secondary content.

| | iOS | macOS | Android | Linux |
|---|---|---|---|---|
| Surfaces | neutral | neutral | Material 3 tonal | neutral |
| Controls | 44 pt, no ink | compact, no ink | Material, ink | compact, no ink |
| Confirmations | system alert | system alert | Material alert | Material alert |
| Sessions, providers, models | bottom sheet | dialog | bottom sheet on phones, dialog on tablets | dialog |
| Safety approval | bottom sheet | centred panel | bottom sheet on phones | centred panel |

Below 720 logical pixels the composer stacks its controls under the message and sends with an icon; the header switches to icon buttons below 820.

## Image paste

Images can be attached with the image picker or pasted directly into the prompt on macOS, web, and Android. Clipboard images use the same preview, four-image, and 10 MiB total limits as picked files; ordinary text paste is preserved.

## Android FCM

Place the Firebase Android configuration at `android/app/google-services.json`; its package must be `com.tingouw.forge`. The file is ignored by Git. Build and install with:

```bash
flutter build apk --release
adb install -r build/app/outputs/flutter-apk/app-release.apk
```

At launch Forge registers its FCM token with `https://forge-push.tingouw.com` using the Android Keystore P-256 identity. Agent pairing and notification scopes are shared with APNs. Notification taps route to the associated agent connection and session.

Android does not keep the WebSocket alive with a foreground service. FCM delivers approval/completion alerts while Forge is backgrounded or removed from Recents; tapping an alert reopens Forge and reconnects. Android Settings → Force stop disables FCM until Forge is opened again.

## macOS production APNs

The direct-download macOS build uses `Developer ID Application`, a Mac Team Direct provisioning profile, and `com.apple.developer.aps-environment = production`. Build it with:

```bash
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer ./scripts/build-macos-app.sh
```

Xcode must be signed into team `QJ6C3M6J85` and have permission to manage profiles. The script compiles unsigned, applies the Developer ID identity and APNs entitlement, then uses Xcode's Developer ID export workflow to create/embed the restricted-entitlement profile. It fails if the final profile does not authorize production APNs.

Public distribution still requires notarization. Configure a notarytool keychain profile, then submit and staple:

```bash
xcrun notarytool submit dist/macos/Forge-macOS.zip --keychain-profile forge-notary --wait
xcrun stapler staple dist/macos/Forge.app
```

### Encrypted Android notifications

Android push content uses per-agent/device AES-256-GCM keys provisioned through the already encrypted WebSocket. Forge verifies the agent's P-256 signature before importing the key into Android Keystore. A native `FirebaseMessagingService` decrypts data-only FCM messages before posting local notifications; no background Dart VM receives ciphertext. Unknown keys, modified metadata, and modified ciphertext are rejected without displaying a notification.

### Encrypted Apple notifications

iOS includes `ForgeNotificationService`, which authenticates/decrypts mutable-content APNs envelopes before replacing the generic alert, using a shared Keychain access group. macOS intentionally has no service extension: while Forge is running but unfocused, its app delegate receives opaque background APNs, decrypts with a mode-0600 key record, and posts a local notification. Closed-app macOS push is not targeted. Unknown keys or modified envelopes are rejected.

### Classifier summaries

The configured safety classifier produces one privacy-bounded sentence for every approval request and completed assistant turn. Forge persists completed-turn summaries for the session picker (time + summary only; UUIDs remain internal) and uses the same sentence as the body of end-to-end encrypted APNs/FCM notifications. Relay infrastructure sees only ciphertext and opaque event metadata.

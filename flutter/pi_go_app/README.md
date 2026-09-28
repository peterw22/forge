# Forge client

One Flutter project that builds Forge for iOS, Android, macOS, Linux and the web. It connects to [`pi-go-agent`](../../docs/agent.md).

| Platform | Connects to | Notifications |
|---|---|---|
| iOS | A remote agent | Push, while closed |
| Android | A remote agent | Push, while closed |
| macOS | A remote agent, or one the app starts itself | Push, while running |
| Linux | A remote agent, or one the native app starts itself | Desktop, while running |
| Web | A remote agent, over `wss://` only | Push, while closed |

Minimum versions: iOS 15, macOS 12.

## Run

For local work on macOS or Linux, run `./scripts/run-desktop.sh` from the
repository root, then choose **Local**, a workspace and **Connect**. No remote
agent or device pairing is required. Provider login is configured separately.

To connect to an independently running network agent, start it, then the client:

```bash
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd /path/to/project

cd flutter/pi_go_app
flutter pub get
flutter run -d macos      # or: -d <device>
```

The agent requires your device on its whitelist. In Forge, open the settings menu, choose **Copy device whitelist entry**, and add it to `~/.pi-go/authorized-devices.json`. See [device authentication](../../docs/security/device-authentication.md#setting-it-up).

## Test

```bash
flutter analyze
flutter test
```

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

## Features

- Streaming transcript with Markdown, a Thinking panel, and tool calls as folded panels that show an activity indicator while they run.
- File writes shown as highlighted source, replacements as a diff.
- Approval of operations the [safety gate](../../docs/security/safety-gate.md) holds.
- Sessions: list, switch, create and name. `Ctrl-B` then `s` opens the list.
- A working directory for each session, chosen from the agent's directories when the session starts.
- Model, thinking level and classifier model, per session.
- Up to two connections at once, each with its own session.
- Reconnects with increasing delay and reattaches to the session it left.
- [Browser live view](../../docs/browser.md#live-view) with manual control.
- **YOLO**, which turns the safety gate off for one session. It can only be changed while the session is idle.

### Images

Attach images with the picker, or paste them into the prompt on macOS, the web and Android. At most four images and 10 MiB in total.

### Fonts

Latin, Greek and Cyrillic use the system font. Fonts for other scripts are downloaded the first time such text is displayed.

## Screenshots

The screenshots in the main README come from the real app driving a real agent:

```bash
python3 scripts/take-screenshots.py          # a phone, in the iOS simulator
python3 scripts/take-screenshots.py --web    # a desktop window, in Chrome
```

The script starts a demo agent with its own configuration, port and sample project, runs Forge against it, and saves the pictures in `dist/screenshots/`. Your own agent and app are not touched.

- The demo agent uses Claude Code, so the run uses the account `claude` is signed in to.
- The app is driven by `tool/screenshots/main.dart`. It rejects every request for approval, except a change to a file of the sample project and running its tests.
- `--web` opens a temporary Cloudflare tunnel, because the web client only connects over TLS. The agent still accepts only the demo device.

## Icon

The icon is drawn by `scripts/generate-icons.py`, which writes it in every size the platforms need and keeps a copy in `design/icon/`. Change the drawing in the script, then run:

```bash
python3 scripts/generate-icons.py
```

It needs Chrome or Chromium, and macOS for `sips`.

## macOS

```bash
./scripts/build-macos-app.sh
# dist/macos/Forge.app
# dist/macos/Forge-macOS.zip
```

Run from the repository root. Xcode is required.

The packaged app contains a universal `pi-go-agent` and offers a **Local** connection that starts it in the chosen workspace (your home directory by default), without device pairing. Disconnecting a local connection stops that agent and any work in progress; Forge warns first. An independent Unix or remote agent is never stopped by disconnecting. For development use `./scripts/run-desktop.sh`; plain `flutter run` does not package the helper.

The app is not sandboxed, because the agent it starts runs shell commands.

### Signing

The script looks for a signing identity in this order: `Developer ID Application`, `Apple Development`, then ad hoc. `SIGN_IDENTITY` overrides the choice and `TEAM_ID` the team.

| Identity | Runs on |
|---|---|
| Developer ID Application, notarized | Any Mac |
| Apple Development, ad hoc | The Mac that built it; elsewhere Gatekeeper blocks it |

```bash
codesign -dv --verbose=4 dist/macos/Forge.app
spctl --assess --type execute --verbose=2 dist/macos/Forge.app
xcrun notarytool submit dist/macos/Forge-macOS.zip --keychain-profile <profile> --wait
xcrun stapler staple dist/macos/Forge.app
```

`codesign --verify` checks that the signature is intact. `spctl --assess` checks that Gatekeeper accepts it.

Push on macOS needs a Developer ID build with a provisioning profile that authorizes production push. The script fails if the profile does not.

## iOS

```bash
flutter build ipa --release --export-method app-store
```

Every upload needs a new build number. Debug and Profile builds use the sandbox push environment, Release builds the production one.

[`s3/`](../../s3/README.md) holds the script that builds an Ad Hoc release and publishes it for installation from a web page.

## Android

```bash
flutter build apk --release
adb reverse tcp:7346 tcp:7346
adb install -r build/app/outputs/flutter-apk/app-release.apk
```

With `adb reverse`, the default address `ws://127.0.0.1:7346/ws` reaches an agent on the development machine.

The release build is signed with the debug key. Configure your own signing before you distribute it.

Push needs a Firebase project. Put its `google-services.json` in `android/app/`; the file is ignored by Git.

## Web

```bash
flutter run -d chrome
flutter build web --wasm --release
```

The web client connects only to `wss://` addresses. The agent itself serves `ws://`, so put it behind a tunnel or a proxy that provides TLS.

Browsers that support it load the WebAssembly build; others fall back to JavaScript. [`s3/`](../../s3/README.md) holds the script that publishes it.

## Linux

See [native Linux desktop](../../docs/linux-desktop.md) for the non-Flatpak
bundle, installation and local agents. Build on Linux:

```bash
./scripts/build-linux-app.sh  # from the repository root
```

For native development on Linux or macOS, `./scripts/run-desktop.sh` builds the
Go helper and starts Flutter with it. A plain `flutter run` needs an explicit
absolute `PI_GO_AGENT_BIN` pointing at a freshly built helper.

The [legacy Flatpak](../../flatpak/README.md) is remote-only. The Linux client
cannot be built on macOS.

## Notifications

Notification text is encrypted by the agent and decrypted on the device. The relay, Apple, Google and the push service of a browser carry ciphertext. See [the push relay](../../docs/security/push-relay.md).

| Platform | Decrypted by |
|---|---|
| iOS | `ForgeNotificationService`, a notification service extension |
| Android | A native messaging service; no Dart code runs |
| macOS | The app, while it is running. A closed app receives nothing |
| Web | The service worker, `web/forge_push.js`; no Dart code runs |

Nothing is shown for an unknown key or a message that fails to decrypt.

Tapping a notification opens the connection and session it belongs to. Notifications for the session you are looking at are suppressed.

### In a browser

A browser notifies only after **Turn on notifications** in the settings menu, because it asks for permission only in answer to a tap. Safari on an iPhone or iPad offers this only to Forge on the Home Screen, which is a device of its own, with its own entry in the agent's whitelist.

Two rules of the apps do not hold here, because a browser withdraws the subscription of a site that receives a push and shows nothing:

- A message that cannot be decrypted is shown as "Encrypted notification", with the reason.
- A notification is shown for the session you are looking at too.

The key that decrypts is kept by the browser as bytes, which the service worker reads while an iPhone is locked too. No hardware protects it. See [the push relay](../../docs/security/push-relay.md#in-a-browser).

```bash
node --test test/forge_push_test.mjs
flutter test --platform chrome test/web_push_test.dart
flutter test --platform chrome --wasm test/web_push_test.dart test/web_identity_test.dart
```

Chrome runs Forge compiled to WebAssembly and Safari compiled to JavaScript, and the two differ in how a list of bytes reaches the browser, so the tests of the web client run under both.

`node --test test/web_policy_test.mjs` tests the policy of the page against what Forge does in a browser.

`node scripts/test-web-push.cjs` tests the whole path in a demo of its own: a relay with its own database and key, an agent with its own configuration, and Chrome with an empty profile. The web client turns notifications on, pairs, and has the agent finish a turn; the notification travels through the push service of Chrome to the service worker. Before that it attaches an image that it picks, under the policy of the page. The run uses Claude Code for one short turn.

Android does not keep a connection open in the background. After **Force stop** in Android's settings, push is off until Forge is opened again.

### Summaries

The text of a notification is one sentence, written by the safety classifier under rules that keep commands, secrets and personal paths out of it. The same sentence labels the session in the session list.

## Building your own

The app identifier, the Apple team and the relay address in this project belong to the maintainer. See [running your own deployment](../../docs/deployment.md).

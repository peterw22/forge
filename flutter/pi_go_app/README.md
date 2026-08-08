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

Full Xcode is required for macOS builds.

For browser use, start the WebSocket endpoint and run Flutter Web:

```bash
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd /path/to/project
flutter run -d chrome
# or: flutter build web --release
```

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

## Image paste

Images can be attached with the image picker or pasted directly into the prompt on macOS, web, and Android. Clipboard images use the same preview, four-image, and 10 MiB total limits as picked files; ordinary text paste is preserved.

# Running your own deployment

The agent runs on its own. The clients and the relay, as they are in this repository, are configured for the maintainer's accounts. To build and publish your own, replace the values below.

## What is specific to one deployment

| Value | In this repository | Where |
|---|---|---|
| App identifier | `com.tingouw.forge` | Xcode projects, Android manifest and Gradle files, Flatpak manifest, entitlements, `worker-push/wrangler.toml` |
| Apple team | `QJ6C3M6J85` | Xcode projects, `flutter/pi_go_app/macos/DeveloperIDExportOptions.plist`, `scripts/add-notification-extensions.rb` |
| Push relay address | `https://forge-push.tingouw.com` | Set `PI_GO_PUSH_RELAY_URL` for the agent and build the client with `--dart-define=FORGE_PUSH_RELAY_URL=…` |
| Relay database | A D1 database ID | `worker-push/wrangler.toml` |
| Firebase project | `forge-2aaf5` | `worker-push/wrangler.toml` |
| Download sites | `forge-app.tingouw.com`, `forge.tingouw.com` | `s3/` |

None of these is a secret. An app identifier and a team are visible in every app that is distributed.

## What is never in the repository

| Secret | Kept |
|---|---|
| Apple push key, Firebase service account, token encryption key | As secrets of the relay |
| `google-services.json` | Locally, in `flutter/pi_go_app/android/app/` |
| Provisioning profiles, signing certificates | In your keychain and Apple account |
| Ad Hoc export options, device identifiers | Locally, in `s3/`. See [`s3/README.md`](../s3/README.md) |
| Agent and device identity keys, provider credentials | On the machines that use them |

`.gitignore` covers the files above.

## The agent alone

```bash
go build -o pi-go-agent ./cmd/pi-go-agent
PI_GO_PUSH_DISABLED=true ./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd "$PWD"
```

This contacts no service of the maintainer. Notifications are off.

## The relay

See [`worker-push/README.md`](../worker-push/README.md). Point the agent at it with `PI_GO_PUSH_RELAY_URL`, and build the client with `--dart-define=FORGE_PUSH_RELAY_URL=https://your-relay`. An empty value builds a client without push.

A push service delivers only to apps signed by the account that owns the push credentials. Your relay therefore needs your build of the clients.

## The clients

| Platform | Guide |
|---|---|
| iOS, Android, macOS, web | [`flutter/pi_go_app/README.md`](../flutter/pi_go_app/README.md) |
| Linux | [`flatpak/README.md`](../flatpak/README.md) |
| Publishing iOS and web builds | [`s3/README.md`](../s3/README.md) |

The web client and the Linux client need no signing and work with any agent.

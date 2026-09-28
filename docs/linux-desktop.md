# Native Linux desktop

Forge uses the standard GTK/Flutter Linux runner: compiled Dart, native plugins
and GPU rendering, with no Chrome, Electron, webview or Flatpak runtime.
The checked Flutter 3.47.5 GTK embedder uses OpenGL (with Impeller), not Vulkan.
Do not set `FLUTTER_LINUX_RENDERER=vulkan`: this embedder does not support it.

## Build

Build on Linux, on the architecture you intend to distribute (x86-64 or ARM64).
Use Flutter 3.47.5 and the Go version in `go.mod`. On Ubuntu install:

```bash
sudo apt-get install clang cmake ninja-build pkg-config libgtk-3-dev \
  libstdc++-12-dev libsecret-1-dev libjsoncpp-dev libnotify-dev
./scripts/build-linux-app.sh
```

The clipboard plugin uses CargoKit; its native Rust toolchain must be available
or downloadable during the build. The output is
`dist/linux/Forge-linux-amd64.tar.gz` or `Forge-linux-arm64.tar.gz`.
Tagged `app-vX.X.X` releases (or `app-vX.X.X-<suffix>` prereleases) additionally
build DEB/RPM packages for both architectures and macOS DMGs; see
[desktop releases](releasing.md#desktop-app-releases).
Build on the oldest distribution you support: glibc/GTK dependencies are not
made portable merely by archiving them. GPU drivers remain host-provided.

Extract the archive into a permanent directory, then run `./forge` or
`./install-user.sh` there. The latter registers an absolute-path desktop launcher
and a `~/.local/bin/forge` symlink, without root. Keep the entire bundle together,
including `lib/`, `data/` and `helpers/`. Do not move it after registration without
rerunning the installer. To uninstall, remove the symlink, the
`com.tingouw.forge.desktop` entry in `$XDG_DATA_HOME/applications` (default
`~/.local/share/applications`), its icon, and the extracted directory.

This is an unsandboxed application running with your ordinary user's privileges;
it does not bypass filesystem permissions, SELinux or AppArmor. Keep agent
safety approvals enabled. Native desktop integration should be tested on both
Wayland and X11. Notifications currently use the desktop portal, which can also
serve unsandboxed apps.

## Local agents (Linux and macOS)

Select **Local**, enter an existing absolute workspace directory, then **Connect**.
Forge asks before starting its bundled Go agent, communicating over private
stdin/stdout. No whitelist, server trust prompt, listening port or independently
installed service is needed. Provider login/API configuration is still required
for model requests; this is separate from device pairing.

Disconnecting stops this owned agent. Closing/crashing the UI closes its stdin;
the agent exits and shuts down its runtime. Local selections are remembered but
are not automatically restarted on app launch. Never run Forge as root.

For development, use the repository-root launcher on either desktop:

```bash
./scripts/run-desktop.sh
```

It builds a matching helper and sets `PI_GO_AGENT_BIN` before `flutter run`.
Plain `flutter run` does not bundle the Go helper. Alternatively:

```bash
go build -o "$PWD/pi-go-agent" ./cmd/pi-go-agent
export PI_GO_AGENT_BIN="$PWD/pi-go-agent"
cd flutter/pi_go_app
flutter run -d linux  # macos on a Mac
```

The override must be absolute. Forge does not execute helpers discovered in the
workspace or on PATH. The child inherits your environment with common user tool
locations added to PATH; desktop launchers do not automatically source shell rc
files. Custom tool locations must be provided in your launch environment.

## Independent same-user Unix agent

Use a separate agent if jobs must survive disconnecting/closing the desktop UI:

```bash
./pi-go-agent --listen "unix://$HOME/.pi-go/agent.sock" \
  --unix-peer-auth --cwd /absolute/workspace
```

In Forge choose **Unix**, enter `$HOME/.pi-go/agent.sock` expanded to its absolute
path, and connect. The helper can also accept a `unix:///absolute/path` address.

`--unix-peer-auth` explicitly replaces device pairing with OS identity:

- The server verifies each client's effective UID through kernel peer credentials.
- Forge uses its bundled helper's `--connect-unix` bridge to verify the server's
  UID before forwarding any bytes. A pathname alone is never proof of identity.
- The socket is mode `0600`; use a private directory such as `~/.pi-go`.
- Same-user IPC is plaintext and trusts **all processes running as that user**.
  It is not isolation from malicious software running under your account.
- Disconnect closes only the bridge/socket, not the independent server.
- Without the flag, Unix listeners retain device pairing and encryption.
- TCP/WebSocket, including localhost, always retain device pairing and encryption.

The legacy Flatpak build remains remote-only. Its restrictions are now applied
only when actually running inside Flatpak, not to every Linux process.

## Regression tests

Build a fresh helper, then use an isolated agent configuration:

```bash
go build -o "$PWD/flutter/pi_go_app/build/local-agent/pi-go-agent" ./cmd/pi-go-agent
export PI_GO_AGENT_BIN="$PWD/flutter/pi_go_app/build/local-agent/pi-go-agent"
export PI_GO_CONFIG_DIR="$(mktemp -d)"
cd flutter/pi_go_app
flutter test test/local_transport_test.dart
```

These tests exercise real local-agent startup, initial state, selected workspace,
Unix reconnect without server shutdown, and TCP plaintext-downgrade rejection.
They make no model calls. Remove the temporary config directory afterward.

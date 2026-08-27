# Forge Flatpak

The Flatpak build is a **remote frontend only**:

- Portal desktop notifications work while Forge is running; the manifest does
  not grant direct notification-daemon or broad session-bus access.
- No local `pi-go-agent` is bundled or launched.
- Unix socket and raw TCP connection modes are disabled.
- Connections use authenticated, encrypted WebSockets only; loopback endpoints
  (`localhost`, `127.0.0.1`, and `::1`) are rejected.
- A software P-256 device identity is stored inside the app sandbox at
  `$XDG_CONFIG_HOME/forge/device-identity.json` with mode `0600`.
- Clipboard access uses Wayland/X11 through `super_clipboard`.
- The sandbox does not request host filesystem, device, notification, or D-Bus
  permissions. Image selection may therefore depend on the desktop file chooser
  portal/compositor integration available on the target system.

## Build requirements

Build on an x86-64 Linux host (native, VM, or CI) with:

- Flutter 3.44 or newer with Linux desktop enabled
- GTK 3 development packages, Clang, CMake, Ninja, pkg-config
- Flatpak and flatpak-builder
- `org.freedesktop.Platform//24.08`
- `org.freedesktop.Sdk//24.08`

On Fedora:

```bash
sudo dnf install clang cmake ninja-build pkgconf-pkg-config gtk3-devel \
  libsecret-devel jsoncpp-devel libnotify-devel flatpak flatpak-builder
flatpak install --user flathub \
  org.freedesktop.Platform//24.08 org.freedesktop.Sdk//24.08
```

Then:

```bash
./flatpak/package.sh
```

Output:

```text
dist/linux/Forge-x86_64.flatpak
```

Install locally:

```bash
flatpak install --user --reinstall dist/linux/Forge-x86_64.flatpak
flatpak run com.tingouw.forge
```

Inspect permissions:

```bash
flatpak info --show-permissions com.tingouw.forge
```

Expected permissions are network, Wayland/fallback X11, IPC, and DRI only.

## macOS limitation

Flatpak and Flutter Linux native binaries cannot be produced on macOS. This
repository includes the Linux runner, sandbox restrictions, manifest, desktop
metadata, and packaging scripts, but `package.sh` must execute on Linux.

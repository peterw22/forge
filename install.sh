#!/usr/bin/env bash
# Installs pi-go-agent, the agent of Forge, as a service of the current user:
# a systemd user unit on Linux, a launchd agent on macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/peterw22/forge/main/install.sh | bash
#
# It downloads the release named below as RELEASE, built and signed by the
# release workflow of the repository; it compiles nothing.
#
# It asks its questions on the terminal. To run without questions, pass the
# answers: `... | bash -s -- --yes --port 7346 --tunnel quick`. See --help.
set -euo pipefail

REPO="${FORGE_REPO:-peterw22/forge}"
# The release that this script installs. It is set to the tag of a release
# before the release is tagged.
RELEASE="v1.0.1-rc23"
SERVICE="${FORGE_SERVICE:-forge-agent}"
BIN_DIR="${FORGE_BIN_DIR:-$HOME/.local/bin}"
CONFIG_DIR="${PI_GO_CONFIG_DIR:-$HOME/.pi-go}"
LOG_DIR="$CONFIG_DIR/logs"
BIN="$BIN_DIR/pi-go-agent"
DEFAULT_PORT=7346

ACTION=install
VERSION="${FORGE_VERSION:-$RELEASE}"
BINARY=""
HOST=""
PORT=""
WORKSPACE=""
TUNNEL=""
TUNNEL_HOSTNAME=""
DEVICE_FILE=""
LINGER=""
ASSUME_YES=0
INTERACTIVE=0
OS=""
WORK=""

usage() {
  cat <<EOF
Installs pi-go-agent as a service of the current user.

  --host <address>       Address to listen on. 127.0.0.1 is this machine only,
                         0.0.0.0 is every interface.
  --port <port>          Port to listen on. Default $DEFAULT_PORT.
  --cwd <directory>      Where the first session works. Default: your home.
  --tunnel <kind>        none, quick or named. Needs cloudflared.
                         quick: no account; the address changes at a restart.
                         named: your own hostname; needs "cloudflared tunnel login".
  --hostname <name>      The hostname of a named tunnel.
  --device-file <file>   A device entry copied from Forge, to authorize.
  --linger <yes|no>      Linux: start at boot, without anyone logging in.
  --version <tag>        The release to install. Default: $RELEASE.
  --binary <file>        Install this executable instead of a release.
  --yes                  Ask nothing; take the defaults for what is not given.
  --update               Replace the executable of an installed agent and
                         restart it. Its address, tunnel and devices stay.
  --uninstall            Remove the services and the executable.
  --help                 Show this.

Environment: FORGE_BIN_DIR (default ~/.local/bin), PI_GO_CONFIG_DIR (default
~/.pi-go), FORGE_SERVICE (default forge-agent).
EOF
}

say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "$WORK" ]; then rm -rf "$WORK"; fi
}

# ask <question> [default] leaves the answer in REPLY.
ask() {
  local question="$1" default="${2:-}"
  REPLY="$default"
  [ "$INTERACTIVE" = 1 ] || return 0
  if [ -n "$default" ]; then
    printf '%s [%s]: ' "$question" "$default" >/dev/tty
  else
    printf '%s: ' "$question" >/dev/tty
  fi
  IFS= read -r REPLY </dev/tty || REPLY=""
  [ -n "$REPLY" ] || REPLY="$default"
}

# confirm <question> <default: yes|no>
confirm() {
  local hint="y/N"
  [ "$2" = yes ] && hint="Y/n"
  while :; do
    ask "$1 ($hint)" ""
    case "${REPLY:-$2}" in
      y | Y | yes | Yes | YES) return 0 ;;
      n | N | no | No | NO) return 1 ;;
    esac
    [ "$INTERACTIVE" = 1 ] || return 1
  done
}

parse_options() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --host | --port | --cwd | --tunnel | --hostname | --device-file | --linger | --version | --binary)
        [ $# -ge 2 ] || fail "$1 needs a value."
        case "$1" in
          --host) HOST="$2" ;;
          --port) PORT="$2" ;;
          --cwd) WORKSPACE="$2" ;;
          --tunnel) TUNNEL="$2" ;;
          --hostname) TUNNEL_HOSTNAME="$2" ;;
          --device-file) DEVICE_FILE="$2" ;;
          --linger) LINGER="$2" ;;
          --version) VERSION="$2" ;;
          --binary) BINARY="$2" ;;
        esac
        shift 2
        ;;
      --yes | -y)
        ASSUME_YES=1
        shift
        ;;
      --update)
        ACTION=update
        shift
        ;;
      --uninstall)
        ACTION=uninstall
        shift
        ;;
      --help | -h)
        usage
        exit 0
        ;;
      *) fail "Unknown option $1. See --help." ;;
    esac
  done
}

# ---------------------------------------------------------------- platform

detect_platform() {
  case "$(uname -s)" in
    Linux) OS=linux ;;
    Darwin) OS=macos ;;
    *) fail "pi-go-agent is released for Linux and macOS, not for $(uname -s)." ;;
  esac
  if [ "$(id -u)" = 0 ] && [ "${FORGE_ALLOW_ROOT:-}" != 1 ]; then
    fail "Run this as the user whose files the agent works on, not as root."
  fi
}

archive_suffix() {
  if [ "$OS" = macos ]; then
    echo macos-universal
    return
  fi
  case "$(uname -m)" in
    x86_64 | amd64) echo linux-amd64 ;;
    aarch64 | arm64) echo linux-arm64 ;;
    *) fail "No release is built for $(uname -m)." ;;
  esac
}

# On Linux the user's systemd must be reachable. A session opened with su or
# by a script may lack the variables that lead to it.
systemd_reachable() {
  systemctl --user show-environment >/dev/null 2>&1
}

prepare_systemd() {
  command -v systemctl >/dev/null 2>&1 || fail "systemd was not found. Start the agent yourself: $BIN --listen ws://127.0.0.1:$DEFAULT_PORT/ws"
  if [ -z "${XDG_RUNTIME_DIR:-}" ]; then
    export XDG_RUNTIME_DIR="/run/user/$(id -u)"
  fi
  if [ -z "${DBUS_SESSION_BUS_ADDRESS:-}" ] && [ -S "$XDG_RUNTIME_DIR/bus" ]; then
    export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
  fi
}

# ---------------------------------------------------------------- questions

valid_ipv4() {
  printf '%s' "$1" | awk -F. 'NF != 4 { exit 1 } { for (i = 1; i <= 4; i++) if ($i !~ /^[0-9]+$/ || $i > 255) exit 1 }'
}

valid_port() {
  case "$1" in
    '' | *[!0-9]*) return 1 ;;
  esac
  [ "$1" -ge 1 ] && [ "$1" -le 65535 ] || return 1
  if [ "$OS" = linux ] && [ "$1" -lt 1024 ]; then return 1; fi
}

# Prints "address interface" for each IPv4 address that is not loopback.
list_addresses() {
  if [ "$OS" = linux ] && command -v ip >/dev/null 2>&1; then
    ip -o -4 addr show 2>/dev/null | awk '{ split($4, a, "/"); if (a[1] !~ /^127\./) print a[1], $2 }'
  else
    ifconfig 2>/dev/null | awk '
      /^[A-Za-z0-9]/ { name = $1; sub(/:$/, "", name) }
      $1 == "inet" { address = $2; sub(/^addr:/, "", address); if (address !~ /^127\./) print address, name }'
  fi
}

port_in_use() {
  local host="$1"
  [ "$host" = 0.0.0.0 ] && host=127.0.0.1
  (exec 3<>"/dev/tcp/$host/$2") 2>/dev/null
}

service_installed() {
  if [ "$OS" = linux ]; then
    [ -f "$HOME/.config/systemd/user/$SERVICE.service" ]
  else
    [ -f "$HOME/Library/LaunchAgents/com.tingouw.$SERVICE.plist" ]
  fi
}

choose_host() {
  if [ -n "$HOST" ]; then
    valid_ipv4 "$HOST" || fail "--host must be an IPv4 address."
    return
  fi
  HOST=127.0.0.1
  [ "$INTERACTIVE" = 1 ] || return 0
  local addresses count=1 line
  addresses="$(list_addresses)"
  {
    echo
    echo "Which address should the agent listen on?"
    printf '  %2d) %-16s %s\n' 1 127.0.0.1 "this machine only; enough with a tunnel"
    if [ -n "$addresses" ]; then
      while IFS= read -r line; do
        count=$((count + 1))
        printf '  %2d) %-16s %s\n' "$count" "${line%% *}" "${line#* }"
      done <<EOF
$addresses
EOF
    fi
    count=$((count + 1))
    printf '  %2d) %-16s %s\n' "$count" 0.0.0.0 "every interface"
  } >/dev/tty
  while :; do
    ask "A number, or an address" 1
    case "$REPLY" in
      1) HOST=127.0.0.1 ;;
      "$count") HOST=0.0.0.0 ;;
      *[!0-9]* | '') HOST="$REPLY" ;;
      *) HOST="$(printf '%s\n' "$addresses" | sed -n "$((REPLY - 1))p" | awk '{ print $1 }')" ;;
    esac
    if [ -n "$HOST" ] && valid_ipv4 "$HOST"; then break; fi
    say "That is neither a number of the list nor an IPv4 address." >/dev/tty
  done
}

choose_port() {
  if [ -n "$PORT" ]; then
    valid_port "$PORT" || fail "--port must be a port this user may open."
  fi
  while :; do
    if [ -z "$PORT" ]; then
      ask "Which port should it listen on?" "$DEFAULT_PORT"
      PORT="$REPLY"
    fi
    if ! valid_port "$PORT"; then
      say "That is not a port this user may open." >/dev/tty
      PORT=""
      continue
    fi
    if port_in_use "$HOST" "$PORT"; then
      if service_installed; then
        say "Port $PORT is in use, which the agent that is being replaced explains."
      elif [ "$INTERACTIVE" = 1 ] && ! confirm "Port $PORT is in use by another program. Use it anyway?" no; then
        PORT=""
        continue
      else
        warn "Port $PORT is in use by another program."
      fi
    fi
    break
  done
}

choose_workspace() {
  if [ -z "$WORKSPACE" ]; then
    ask "Where should the first session work?" "$HOME"
    WORKSPACE="$REPLY"
  fi
  case "$WORKSPACE" in
    "~") WORKSPACE="$HOME" ;;
    "~/"*) WORKSPACE="$HOME/${WORKSPACE#\~/}" ;;
  esac
  [ -d "$WORKSPACE" ] || fail "$WORKSPACE is not a directory."
  WORKSPACE="$(cd "$WORKSPACE" && pwd)"
}

choose_tunnel() {
  local found=0
  command -v cloudflared >/dev/null 2>&1 && found=1
  if [ -z "$TUNNEL" ]; then
    TUNNEL=none
    if [ "$found" = 1 ] && [ "$INTERACTIVE" = 1 ]; then
      {
        echo
        echo "cloudflared is installed. A tunnel gives the agent a wss:// address,"
        echo "which Forge in a browser needs and which works from any network."
        echo "   1) Quick tunnel   no account; the address changes when the tunnel restarts"
        echo "   2) Named tunnel   a hostname of yours, which stays; needs \"cloudflared tunnel login\""
        echo "   3) No tunnel"
      } >/dev/tty
      while :; do
        ask "A number" 1
        case "$REPLY" in
          1) TUNNEL=quick ;;
          2) TUNNEL=named ;;
          3) TUNNEL=none ;;
          *) continue ;;
        esac
        break
      done
    fi
  fi
  case "$TUNNEL" in
    none) return ;;
    quick | named) [ "$found" = 1 ] || fail "--tunnel $TUNNEL needs cloudflared, which is not installed." ;;
    *) fail "--tunnel must be none, quick or named." ;;
  esac
  if [ "$TUNNEL" = quick ]; then
    if [ -f "$HOME/.cloudflared/config.yml" ] || [ -f "$HOME/.cloudflared/config.yaml" ]; then
      fail "cloudflared starts no quick tunnel while ~/.cloudflared has a configuration file. Choose a named tunnel."
    fi
    return
  fi
  [ -f "$HOME/.cloudflared/cert.pem" ] || fail "A named tunnel needs your Cloudflare account. Run \"cloudflared tunnel login\", then this again."
  if [ -z "$TUNNEL_HOSTNAME" ]; then
    ask "Which hostname should lead to the agent, such as forge.example.com?" ""
    TUNNEL_HOSTNAME="$REPLY"
  fi
  printf '%s' "$TUNNEL_HOSTNAME" | grep -Eq '^([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z]{2,}$' ||
    fail "A named tunnel needs a hostname, such as forge.example.com."
}

choose_linger() {
  [ "$OS" = linux ] || return 0
  case "$LINGER" in
    yes | no) return ;;
    '') ;;
    *) fail "--linger must be yes or no." ;;
  esac
  LINGER=no
  if [ "$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null)" = yes ]; then
    LINGER=yes
    return
  fi
  [ "$INTERACTIVE" = 1 ] || return 0
  {
    echo
    echo "A service of a user starts when the user logs in and stops when they log out."
    echo "A machine without a screen needs the agent from boot."
  } >/dev/tty
  if confirm "Start the agent at boot, without anyone logging in?" yes; then LINGER=yes; fi
}

# ---------------------------------------------------------------- download

fetch() {
  curl --fail --silent --show-error --location --retry 3 "$@"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{ print $1 }'
  else
    fail "Neither sha256sum nor shasum was found, so the download cannot be checked."
  fi
}

install_binary() {
  local source
  if [ -n "$BINARY" ]; then
    [ -f "$BINARY" ] || fail "$BINARY is not a file."
    step "Installing $BINARY"
    source="$BINARY"
  else
    command -v curl >/dev/null 2>&1 || fail "curl was not found."
    command -v tar >/dev/null 2>&1 || fail "tar was not found."
    local name archive expected actual
    name="pi-go-agent-$VERSION-$(archive_suffix)"
    archive="$name.tar.gz"
    step "Downloading pi-go-agent $VERSION"
    fetch -o "$WORK/$archive" "https://github.com/$REPO/releases/download/$VERSION/$archive" ||
      fail "Release $VERSION of $REPO has no $archive. See https://github.com/$REPO/releases"
    fetch -o "$WORK/SHA256SUMS" "https://github.com/$REPO/releases/download/$VERSION/SHA256SUMS" ||
      fail "Release $VERSION of $REPO has no SHA256SUMS."
    expected="$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1 }' "$WORK/SHA256SUMS")"
    actual="$(sha256_of "$WORK/$archive")"
    [ -n "$expected" ] || fail "SHA256SUMS of $VERSION does not list $archive."
    [ "$expected" = "$actual" ] || fail "The checksum of $archive is wrong. Nothing was installed."
    say "The checksum is right."
    tar -C "$WORK" -xzf "$WORK/$archive"
    source="$WORK/$name/pi-go-agent"
    [ -f "$source" ] || fail "$archive holds no pi-go-agent."
  fi
  mkdir -p "$BIN_DIR"
  # Renaming replaces the executable of a running agent without disturbing it.
  cp "$source" "$BIN.new"
  chmod 0755 "$BIN.new"
  mv -f "$BIN.new" "$BIN"
  "$BIN" --help >/dev/null 2>&1 || fail "$BIN does not run on this machine."
  say "Installed $BIN"
}

# ---------------------------------------------------------------- devices

# Reads one pasted JSON object from the terminal into DEVICE_ENTRY. An empty
# line instead of an entry leaves it empty.
read_device_entry() {
  local line depth=0 opens closes
  DEVICE_ENTRY=""
  while IFS= read -r line </dev/tty; do
    if [ -z "$DEVICE_ENTRY" ] && [ -z "$(printf '%s' "$line" | tr -d '[:space:]')" ]; then
      return 0
    fi
    DEVICE_ENTRY="$DEVICE_ENTRY$line
"
    opens="$(printf '%s' "$line" | tr -cd '{' | wc -c | tr -d ' ')"
    closes="$(printf '%s' "$line" | tr -cd '}' | wc -c | tr -d ' ')"
    depth=$((depth + opens - closes))
    [ "$depth" -gt 0 ] || break
  done
}

authorize_devices() {
  local whitelist="$CONFIG_DIR/authorized-devices.json" can_authorize=0 help
  mkdir -p "$CONFIG_DIR"
  chmod 0700 "$CONFIG_DIR"
  if [ ! -f "$whitelist" ]; then
    (umask 077 && printf '{"version":1,"devices":[]}\n' >"$whitelist")
  fi
  help="$("$BIN" --help 2>&1)" || true
  case "$help" in
    *-authorize-device*) can_authorize=1 ;;
  esac

  if [ -n "$DEVICE_FILE" ]; then
    [ -f "$DEVICE_FILE" ] || fail "$DEVICE_FILE is not a file."
    [ "$can_authorize" = 1 ] || fail "pi-go-agent $VERSION cannot add a device yet. Add the entry to $whitelist by hand."
    step "Authorizing the device"
    "$BIN" --authorize-device --authorized-devices "$whitelist" <"$DEVICE_FILE" ||
      fail "The device of $DEVICE_FILE was not authorized."
    return
  fi
  [ "$INTERACTIVE" = 1 ] || return 0
  step "Your first device"
  if [ "$can_authorize" = 0 ]; then
    say "pi-go-agent $VERSION cannot add a device yet. Add the entry that Forge"
    say "copies to the devices of $whitelist by hand."
    return
  fi
  say "Only a device that is listed may connect. In Forge, open the settings menu"
  say "and choose \"Copy device whitelist entry\". Paste it here and press Enter."
  say "Press Enter alone to do this later."
  while :; do
    printf '> ' >/dev/tty
    read_device_entry
    if [ -z "$DEVICE_ENTRY" ]; then
      say "No device was added. Nothing can connect until one is."
      return
    fi
    if printf '%s' "$DEVICE_ENTRY" | "$BIN" --authorize-device --authorized-devices "$whitelist"; then
      return
    fi
    say "That entry was not accepted. Paste it again, or press Enter alone to skip."
  done
}

# ---------------------------------------------------------------- tunnel

prepare_named_tunnel() {
  local id
  step "Setting up the tunnel to $TUNNEL_HOSTNAME"
  id="$(cloudflared tunnel list --name "$SERVICE" --output json 2>/dev/null |
    grep -Eo '"id": *"[0-9a-f-]{36}"' | awk -F '"' 'NR == 1 { print $4 }')" || true
  if [ -z "$id" ]; then
    cloudflared tunnel create "$SERVICE"
    id="$(cloudflared tunnel list --name "$SERVICE" --output json |
      grep -Eo '"id": *"[0-9a-f-]{36}"' | awk -F '"' 'NR == 1 { print $4 }')" || true
  fi
  [ -n "$id" ] || fail "The tunnel $SERVICE could not be created."
  [ -f "$HOME/.cloudflared/$id.json" ] ||
    fail "The tunnel $SERVICE belongs to another machine: its credentials, $id.json, are not in ~/.cloudflared. Delete it with \"cloudflared tunnel delete $SERVICE\", or set FORGE_SERVICE to another name."
  cloudflared tunnel route dns "$SERVICE" "$TUNNEL_HOSTNAME" ||
    fail "$TUNNEL_HOSTNAME could not be pointed at the tunnel. If the name has a DNS record already, remove it in the Cloudflare dashboard."
}

# ---------------------------------------------------------------- services

agent_arguments() {
  AGENT_ARGUMENTS=("$BIN" --listen "ws://$HOST:$PORT/ws" --cwd "$WORKSPACE")
  case "$HOST" in
    127.*) ;;
    *) AGENT_ARGUMENTS+=(--allow-remote) ;;
  esac
}

tunnel_arguments() {
  local target="$HOST"
  [ "$target" = 0.0.0.0 ] && target=127.0.0.1
  TUNNEL_ARGUMENTS=("$(command -v cloudflared)" tunnel --no-autoupdate)
  if [ "$TUNNEL" = named ]; then
    TUNNEL_ARGUMENTS+=(run --url "http://$target:$PORT" "$SERVICE")
  else
    TUNNEL_ARGUMENTS+=(--url "http://$target:$PORT")
  fi
}

# The service runs the commands of the model, which need the programs that
# your shell finds.
service_path() {
  case ":$PATH:" in
    *":$BIN_DIR:"*) printf '%s' "$PATH" ;;
    *) printf '%s' "$BIN_DIR:$PATH" ;;
  esac
}

systemd_word() {
  printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/%/%%/g' -e 's/\$/$$/g')"
}

systemd_setting() {
  printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/%/%%/g')"
}

# write_unit <file> <description> <log> <command...>
write_unit() {
  local file="$1" description="$2" log="$3" command="" word
  shift 3
  for word in "$@"; do
    command="$command $(systemd_word "$word")"
  done
  {
    echo "[Unit]"
    echo "Description=$description"
    echo
    echo "[Service]"
    echo "ExecStart=${command# }"
    echo "WorkingDirectory=$(printf '%s' "$WORKSPACE" | sed -e 's/%/%%/g')"
    echo "Environment=$(systemd_setting "PATH=$(service_path)")"
    echo "Environment=$(systemd_setting "PI_GO_CONFIG_DIR=$CONFIG_DIR")"
    echo "StandardOutput=append:$(printf '%s' "$log" | sed -e 's/%/%%/g')"
    echo "StandardError=inherit"
    echo "Restart=always"
    echo "RestartSec=5"
    echo
    echo "[Install]"
    echo "WantedBy=default.target"
  } >"$file"
}

enable_linger() {
  [ "$LINGER" = yes ] || return 0
  if [ "$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null)" = yes ]; then
    return 0
  fi
  if loginctl enable-linger 2>/dev/null || loginctl enable-linger "$(id -un)" 2>/dev/null; then
    say "The agent starts at boot."
    return 0
  fi
  if command -v sudo >/dev/null 2>&1 && [ "$INTERACTIVE" = 1 ]; then
    say "Starting at boot needs an administrator."
    if sudo loginctl enable-linger "$(id -un)" </dev/tty; then
      say "The agent starts at boot."
      return 0
    fi
  fi
  warn "Starting at boot was not set. An administrator can set it: sudo loginctl enable-linger $(id -un)"
  LINGER=no
}

start_linux() {
  local units="$HOME/.config/systemd/user"
  prepare_systemd
  if ! systemd_reachable; then
    # Lingering starts the user's systemd where no session has done so.
    enable_linger
    sleep 2
    systemd_reachable || fail "The systemd of user $(id -un) cannot be reached. Log in over SSH or at the console, not with su, and run this again."
  fi
  mkdir -p "$units" "$LOG_DIR"
  write_unit "$units/$SERVICE.service" "Forge agent" "$LOG_DIR/$SERVICE.log" "${AGENT_ARGUMENTS[@]}"
  if [ "$TUNNEL" != none ]; then
    write_unit "$units/$SERVICE-tunnel.service" "Tunnel to the Forge agent" "$LOG_DIR/$SERVICE-tunnel.log" "${TUNNEL_ARGUMENTS[@]}"
    : >"$LOG_DIR/$SERVICE-tunnel.log"
  else
    systemctl --user disable --now "$SERVICE-tunnel.service" >/dev/null 2>&1 || true
    rm -f "$units/$SERVICE-tunnel.service"
  fi
  systemctl --user daemon-reload
  systemctl --user enable "$SERVICE.service" >/dev/null 2>&1
  systemctl --user restart "$SERVICE.service"
  if [ "$TUNNEL" != none ]; then
    systemctl --user enable "$SERVICE-tunnel.service" >/dev/null 2>&1
    systemctl --user restart "$SERVICE-tunnel.service"
  fi
  enable_linger
}

xml_text() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

# write_plist <file> <label> <log> <command...>
write_plist() {
  local file="$1" label="$2" log="$3" word
  shift 3
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
    echo '<plist version="1.0">'
    echo '<dict>'
    echo "  <key>Label</key><string>$(xml_text "$label")</string>"
    echo '  <key>ProgramArguments</key>'
    echo '  <array>'
    for word in "$@"; do
      echo "    <string>$(xml_text "$word")</string>"
    done
    echo '  </array>'
    echo "  <key>WorkingDirectory</key><string>$(xml_text "$WORKSPACE")</string>"
    echo '  <key>EnvironmentVariables</key>'
    echo '  <dict>'
    echo "    <key>PATH</key><string>$(xml_text "$(service_path)")</string>"
    echo "    <key>PI_GO_CONFIG_DIR</key><string>$(xml_text "$CONFIG_DIR")</string>"
    echo '  </dict>'
    echo '  <key>RunAtLoad</key><true/>'
    echo '  <key>KeepAlive</key><true/>'
    echo '  <key>ThrottleInterval</key><integer>5</integer>'
    echo "  <key>StandardOutPath</key><string>$(xml_text "$log")</string>"
    echo "  <key>StandardErrorPath</key><string>$(xml_text "$log")</string>"
    echo '</dict>'
    echo '</plist>'
  } >"$file"
}

launchd_stop() {
  local label="$1" domain count
  for domain in "gui/$(id -u)" "user/$(id -u)"; do
    launchctl bootout "$domain/$label" >/dev/null 2>&1 || continue
    # bootout returns before the service is gone.
    count=0
    while launchctl print "$domain/$label" >/dev/null 2>&1 && [ "$count" -lt 50 ]; do
      sleep 0.2
      count=$((count + 1))
    done
  done
}

launchd_start() {
  local label="$1" plist="$2"
  launchd_stop "$label"
  # The graphical session is there when you are logged in at the screen. Over
  # SSH alone, only the background session of the user is.
  launchctl bootstrap "gui/$(id -u)" "$plist" 2>/dev/null ||
    launchctl bootstrap "user/$(id -u)" "$plist" ||
    fail "launchd did not accept $plist."
}

start_macos() {
  local agents="$HOME/Library/LaunchAgents"
  mkdir -p "$agents" "$LOG_DIR"
  write_plist "$agents/com.tingouw.$SERVICE.plist" "com.tingouw.$SERVICE" "$LOG_DIR/$SERVICE.log" "${AGENT_ARGUMENTS[@]}"
  launchd_start "com.tingouw.$SERVICE" "$agents/com.tingouw.$SERVICE.plist"
  if [ "$TUNNEL" != none ]; then
    write_plist "$agents/com.tingouw.$SERVICE-tunnel.plist" "com.tingouw.$SERVICE-tunnel" "$LOG_DIR/$SERVICE-tunnel.log" "${TUNNEL_ARGUMENTS[@]}"
    : >"$LOG_DIR/$SERVICE-tunnel.log"
    launchd_start "com.tingouw.$SERVICE-tunnel" "$agents/com.tingouw.$SERVICE-tunnel.plist"
  else
    launchd_stop "com.tingouw.$SERVICE-tunnel"
    rm -f "$agents/com.tingouw.$SERVICE-tunnel.plist"
  fi
}

service_running() {
  if [ "$OS" = linux ]; then
    systemctl --user is-active --quiet "$SERVICE.service"
    return
  fi
  local domain
  for domain in "gui/$(id -u)" "user/$(id -u)"; do
    if launchctl print "$domain/com.tingouw.$SERVICE" 2>/dev/null | grep -q 'state = running'; then
      return 0
    fi
  done
  return 1
}

# An answer on the port proves little alone: another program may hold the port
# while the service fails to open it, again and again. A service that fails so
# is not running a moment later.
agent_answers() {
  curl --fail --silent --max-time 2 -o /dev/null "http://$1:$PORT/healthz" || return 1
  service_running || return 1
  sleep 2
  service_running
}

wait_for_agent() {
  local host="$HOST" count=0
  [ "$host" = 0.0.0.0 ] && host=127.0.0.1
  while [ "$count" -lt 30 ]; do
    if agent_answers "$host"; then
      say "The agent answers on port $PORT."
      return 0
    fi
    sleep 1
    count=$((count + 1))
  done
  if port_in_use "$host" "$PORT" && ! service_running; then
    warn "Another program holds port $PORT, so the agent cannot open it."
  fi
  warn "The agent does not answer. The end of $LOG_DIR/$SERVICE.log:"
  tail -n 20 "$LOG_DIR/$SERVICE.log" >&2 2>/dev/null || true
  return 1
}

quick_tunnel_address() {
  local count=0 address=""
  while [ "$count" -lt 45 ]; do
    address="$(grep -Eo 'https://[a-z0-9-]+\.trycloudflare\.com' "$LOG_DIR/$SERVICE-tunnel.log" 2>/dev/null | tail -n 1)" || true
    [ -z "$address" ] || break
    sleep 1
    count=$((count + 1))
  done
  [ -n "$address" ] || return 1
  echo "wss://${address#https://}/ws"
}

# ---------------------------------------------------------------- summary

summary() {
  local address="" identity
  step "Forge agent is installed"
  case "$TUNNEL" in
    named) address="wss://$TUNNEL_HOSTNAME/ws" ;;
    quick)
      say "Waiting for the address of the tunnel..."
      address="$(quick_tunnel_address)" || warn "The tunnel gave no address. See $LOG_DIR/$SERVICE-tunnel.log"
      ;;
  esac
  say
  say "Connect Forge to:"
  [ -z "$address" ] || say "  $address"
  if [ "$HOST" = 0.0.0.0 ]; then
    list_addresses | while IFS= read -r line; do
      say "  ws://${line%% *}:$PORT/ws"
    done
  elif [ "${HOST#127.}" = "$HOST" ]; then
    say "  ws://$HOST:$PORT/ws"
  elif [ -z "$address" ]; then
    say "  ws://$HOST:$PORT/ws    from this machine only"
  fi
  if [ "$TUNNEL" = quick ]; then
    say
    say "The address of a quick tunnel changes when the tunnel restarts. The current one:"
    say "  grep -Eo 'https://[a-z0-9-]+\\.trycloudflare\\.com' '$LOG_DIR/$SERVICE-tunnel.log' | tail -n 1"
  fi
  identity="$(PI_GO_CONFIG_DIR="$CONFIG_DIR" "$BIN" --print-identity 2>/dev/null | sed -n 's/^P-256 fingerprint: //p')" || true
  if [ -n "$identity" ]; then
    say
    say "Forge shows the fingerprint of the agent at the first connection. It must be:"
    say "  $identity"
  fi
  say
  say "Sign in to a model provider, if you have not:"
  say "  $BIN --login        OpenAI Codex; the others are set up from Forge"
  say
  if [ "$OS" = linux ]; then
    say "Status:     systemctl --user status $SERVICE"
    say "Restart:    systemctl --user restart $SERVICE"
  else
    say "Status:     launchctl print gui/$(id -u)/com.tingouw.$SERVICE"
    say "Restart:    launchctl kickstart -k gui/$(id -u)/com.tingouw.$SERVICE"
  fi
  say "Log:        $LOG_DIR/$SERVICE.log"
  say "Devices:    $CONFIG_DIR/authorized-devices.json"
  say "Update:     curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash -s -- --update"
  say "Uninstall:  curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | bash -s -- --uninstall"
}

# ---------------------------------------------------------------- update

service_file() {
  if [ "$OS" = linux ]; then
    echo "$HOME/.config/systemd/user/$SERVICE.service"
  else
    echo "$HOME/Library/LaunchAgents/com.tingouw.$SERVICE.plist"
  fi
}

update() {
  local address domain restarted=0
  service_installed || fail "No agent is installed under the name $SERVICE. Run this without --update."
  # The service knows where the agent listens.
  address="$(grep -Eo 'ws://[0-9.]+:[0-9]+/' "$(service_file)" | awk 'NR == 1')" || true
  HOST="$(printf '%s' "$address" | sed -e 's|^ws://||' -e 's|:.*||')"
  PORT="$(printf '%s' "$address" | sed -e 's|.*:||' -e 's|/$||')"
  { valid_ipv4 "$HOST" && valid_port "$PORT"; } || fail "$(service_file) does not say where the agent listens. Install again without --update."
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/forge-install.XXXXXX")"
  trap cleanup EXIT
  install_binary
  step "Restarting the service"
  if [ "$OS" = linux ]; then
    prepare_systemd
    systemctl --user restart "$SERVICE.service"
  else
    for domain in "gui/$(id -u)" "user/$(id -u)"; do
      if launchctl kickstart -k "$domain/com.tingouw.$SERVICE" 2>/dev/null; then
        restarted=1
        break
      fi
    done
    [ "$restarted" = 1 ] || launchd_start "com.tingouw.$SERVICE" "$(service_file)"
  fi
  wait_for_agent || fail "The agent was updated but did not start."
  if [ -n "$BINARY" ]; then
    say "The agent is updated."
  else
    say "The agent is updated to $VERSION."
  fi
  if [ -f "$LOG_DIR/$SERVICE-tunnel.log" ] && [ -f "$(service_file | sed -e "s|$SERVICE\.|$SERVICE-tunnel.|")" ]; then
    say "The tunnel was left running, so its address is the same."
  fi
}

# ---------------------------------------------------------------- uninstall

uninstall() {
  step "Removing the Forge agent"
  if [ "$OS" = linux ]; then
    prepare_systemd
    systemctl --user disable --now "$SERVICE-tunnel.service" >/dev/null 2>&1 || true
    systemctl --user disable --now "$SERVICE.service" >/dev/null 2>&1 || true
    rm -f "$HOME/.config/systemd/user/$SERVICE.service" "$HOME/.config/systemd/user/$SERVICE-tunnel.service"
    systemctl --user daemon-reload >/dev/null 2>&1 || true
  else
    launchd_stop "com.tingouw.$SERVICE-tunnel"
    launchd_stop "com.tingouw.$SERVICE"
    rm -f "$HOME/Library/LaunchAgents/com.tingouw.$SERVICE.plist" "$HOME/Library/LaunchAgents/com.tingouw.$SERVICE-tunnel.plist"
  fi
  rm -f "$BIN"
  say "The services and $BIN are removed."
  say "Kept: $CONFIG_DIR, with the keys of the agent, its devices and its logs."
  if command -v cloudflared >/dev/null 2>&1 && [ -f "$HOME/.cloudflared/cert.pem" ]; then
    say "A named tunnel stays in your Cloudflare account. To delete it: cloudflared tunnel delete $SERVICE"
  fi
}

# ---------------------------------------------------------------- main

main() {
  parse_options "$@"
  detect_platform
  if [ "$ACTION" = uninstall ]; then
    uninstall
    return
  fi
  if [ "$ACTION" = update ]; then
    update
    return
  fi
  if [ "$ASSUME_YES" = 0 ]; then
    if { : >/dev/tty; } 2>/dev/null; then
      INTERACTIVE=1
    else
      fail "There is no terminal to ask on. Pass --yes and the answers as options; see --help."
    fi
  fi
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/forge-install.XXXXXX")"
  trap cleanup EXIT
  export PI_GO_CONFIG_DIR="$CONFIG_DIR"

  choose_host
  choose_port
  choose_workspace
  choose_tunnel
  choose_linger

  install_binary
  authorize_devices
  [ "$TUNNEL" != named ] || prepare_named_tunnel

  step "Starting the service"
  agent_arguments
  [ "$TUNNEL" = none ] || tunnel_arguments
  if [ "$OS" = linux ]; then start_linux; else start_macos; fi
  wait_for_agent || fail "The agent was installed but did not start."
  summary
}

# The script runs only once it has arrived whole.
main "$@"

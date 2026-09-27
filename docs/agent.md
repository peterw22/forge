# Running the agent

`pi-go-agent` runs on the machine whose files and shell you want the model to use.

## Install

On Linux or macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/peterw22/forge/main/install.sh | bash
```

To read the script before it runs, download it first:

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/peterw22/forge/main/install.sh
less install.sh
bash install.sh
```

The script installs `pi-go-agent` for your user alone and needs no administrator. It downloads an executable that the [release workflow](releasing.md) built, and compiles nothing. It:

1. asks which address and port the agent listens on;
2. asks where the first session works;
3. offers a tunnel, where `cloudflared` is installed;
4. on Linux, asks whether the agent starts at boot;
5. downloads the release that the script names and compares it with the checksum of the release;
6. asks for your first device;
7. starts the service and prints the address to enter in Forge.

| Installed | Linux | macOS |
|---|---|---|
| The executable | `~/.local/bin/pi-go-agent` | `~/.local/bin/pi-go-agent` |
| The service | `~/.config/systemd/user/forge-agent.service` | `~/Library/LaunchAgents/com.tingouw.forge-agent.plist` |
| The tunnel, if chosen | `forge-agent-tunnel.service` | `com.tingouw.forge-agent-tunnel.plist` |
| The logs | `~/.pi-go/logs/` | `~/.pi-go/logs/` |

The service is restarted when it stops. It runs with the `PATH` of the shell that installed it, so the commands of the model find the programs that you find.

### Your first device

In Forge, open the settings menu and choose **Copy device whitelist entry**. Paste the entry when the script asks. The agent checks that the fingerprint of the entry is that of its key before it lists the device.

To add a device later:

```bash
pbpaste | pi-go-agent --authorize-device        # macOS; any file or pipe will do
systemctl --user restart forge-agent            # Linux
launchctl kickstart -k gui/$(id -u)/com.tingouw.forge-agent   # macOS
```

### Starting at boot

A service of a user starts when the user logs in. On Linux the script offers to start it at boot instead, which a machine without a screen needs. It does so with `loginctl enable-linger`, which may ask for an administrator.

On macOS the service starts when you log in.

### A tunnel

Forge in a browser requires a `wss://` address. Where `cloudflared` is installed, the script offers one:

| Tunnel | Needs | Address |
|---|---|---|
| Quick | Nothing | `wss://<random>.trycloudflare.com/ws`, which changes whenever the tunnel restarts |
| Named | `cloudflared tunnel login` and a domain in your Cloudflare account | `wss://<your hostname>/ws`, which stays |

A tunnel makes the agent reachable from the internet. Only a device that is listed passes the handshake, but read [the internet](#the-internet) first. The named tunnel has been tested against a stand-in for `cloudflared` only.

### Without questions

```bash
curl -fsSL https://raw.githubusercontent.com/peterw22/forge/main/install.sh |
  bash -s -- --yes --host 127.0.0.1 --port 7346 --tunnel quick --device-file device.json
```

| Option | Meaning | Default with `--yes` |
|---|---|---|
| `--host <address>` | The address to listen on; `0.0.0.0` is every interface | `127.0.0.1` |
| `--port <port>` | The port | `7346` |
| `--cwd <directory>` | Where the first session works | Your home |
| `--tunnel <kind>` | `none`, `quick` or `named` | `none` |
| `--hostname <name>` | The hostname of a named tunnel | |
| `--device-file <file>` | A device entry to authorize | |
| `--linger <yes\|no>` | Linux: start at boot | `no` |
| `--version <tag>` | The release to install | The release that the script names |
| `--binary <file>` | Install this executable instead of a release | |
| `--uninstall` | Remove the services and the executable | |

### Removing it

```bash
curl -fsSL https://raw.githubusercontent.com/peterw22/forge/main/install.sh | bash -s -- --uninstall
```

`~/.pi-go` stays, with the keys of the agent, its devices and its logs.

## Build

Go 1.26 or newer.

```bash
go build -o pi-go-agent ./cmd/pi-go-agent
go build -o pi-go-tui ./cmd/pi-go-tui
go test ./...
```

## Sign in to a provider

```bash
./pi-go-agent --login
```

signs in to OpenAI Codex. The other providers are set up from Forge or their own programs. See [providers](README.md#providers).

## One prompt

```bash
./pi-go-agent --model gpt-5.6-terra --prompt "List the modules in this repository"
```

## Serving clients

```bash
# Forge on any platform
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd "$PWD"

# A Unix socket
./pi-go-agent --listen "unix://$HOME/.pi-go/agent.sock" --cwd "$PWD"

# Loopback TCP
./pi-go-agent --listen tcp://127.0.0.1:7346 --cwd "$PWD"

# Standard input and output, for a parent process
./pi-go-agent --serve
```

Every network listener requires [device authentication](security/device-authentication.md). Add your device with `--authorize-device` before you connect; see [your first device](#your-first-device).

A WebSocket listener answers a health check at `/healthz`.

### Other machines on your network

A listener binds to loopback unless you pass `--allow-remote`:

```bash
./pi-go-agent --print-identity
./pi-go-agent --listen ws://192.168.1.20:7346/ws --allow-remote --cwd "$PWD"
```

Compare the fingerprint that `--print-identity` prints with the one Forge shows on the first connection.

### The internet

The web client requires a `wss://` address, which the agent does not serve itself; a tunnel or a proxy that provides TLS supplies it.

Forge's protocol authenticates and encrypts on its own, and has not been independently audited. To reach an agent from outside your network, put it behind a VPN or a tunnel you trust. Read the [threat model](security/threat-model.md) first.

## Options

| Option | Meaning |
|---|---|
| `--listen <address>` | Serve on `ws://`, `tcp://` or `unix://` |
| `--serve` | Serve on standard input and output |
| `--allow-remote` | Permit an address that is not loopback |
| `--authorized-devices <file>` | The device whitelist |
| `--authorize-device` | Add the device entry on standard input to the whitelist and exit |
| `--print-identity` | Print the agent's identity and exit |
| `--cwd <directory>` | The working directory of the first session |
| `--model <id>` | The model |
| `--thinking <level>` | `off`, `minimal`, `low`, `medium`, `high`, `xhigh` or `max` |
| `--prompt <text>` | Run one prompt and exit |
| `--session <file>`, `--resume` | Use or restore a session file |
| `--system-prompt <text>` | Replace the default system prompt |
| `--login`, `--logout` | Sign in to or out of OpenAI Codex |

## Environment

| Variable | Meaning |
|---|---|
| `PI_GO_CONFIG_DIR` | Where keys, credentials and the whitelist are kept. Default `~/.pi-go` |
| `PI_GO_PUSH_RELAY_URL` | The push relay to use |
| `PI_GO_PUSH_DISABLED` | `true` turns push notifications off |
| `PI_GO_CODEX_TOKEN` | An OpenAI Codex token for this process only |
| `PI_GO_AGY_HOME` | The profile used for Antigravity |

## Files

| Path | Contents |
|---|---|
| `~/.pi-go/push/identity.json` | The agent's identity key and the notification keys of paired devices |
| `~/.pi-go/authorized-devices.json` | Devices that may connect |
| `~/.pi-go/auth.json` | Provider credentials |
| `<workspace>/.pi-go/sessions/` | Session files |
| `<workspace>/.pi-go/latest` | The most recent session |

Files under `~/.pi-go` are created with mode `0600` in a directory with mode `0700`.

## Commands in a conversation

Typed into the prompt in Forge:

| Command | Does |
|---|---|
| `/model <id>` | Switches model |
| `/thinking <level>` | Sets reasoning effort |
| `/compact [focus]` | Summarizes older turns |
| `/sessions` | Opens the session list |
| `/new` | Starts a session, after asking where it should work |
| `/name <name>` | Names the session |
| `/abort` | Stops the turn and ends a running tool |
| `/clear` | Clears the visible transcript; the conversation is kept |
| `/help` | Lists commands |

The model, thinking level and compaction cannot be changed while a turn is running.

## Working directory

Each session has its own working directory, stored in its session file.

- `--cwd` is where the first session works, and where session files are kept.
- When Forge starts a session, it asks where the session should work and lets you browse the directories on the agent's machine.
- A session the app opens with asks too, as long as it has no conversation yet.
- The directory stays with the session. To work somewhere else, start a session there.

Forge shows the directory in its header and beside each session in the session list.

## The terminal client

```bash
./pi-go-tui --model gpt-5.6-terra
```

It starts an agent of its own and supports streaming, Markdown, tool output, approvals, `Ctrl-C` to stop a tool, and `Ctrl-B` then `s` to switch session.

## The browser

Browser tools need a driver and a browser on the agent's machine:

```bash
bash scripts/install-browser.sh
```

See [browser](browser.md).

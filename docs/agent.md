# Running the agent

`pi-go-agent` runs on the machine whose files and shell you want the model to use.

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

Every network listener requires [device authentication](security/device-authentication.md). Add your device to `~/.pi-go/authorized-devices.json` before you connect.

A WebSocket listener answers a health check at `/healthz`.

### Other machines on your network

A listener binds to loopback unless you pass `--allow-remote`:

```bash
./pi-go-agent --print-identity
./pi-go-agent --listen ws://192.168.1.20:7346/ws --allow-remote --cwd "$PWD"
```

Compare the fingerprint that `--print-identity` prints with the one Forge shows on the first connection.

### The internet

Forge's protocol authenticates and encrypts on its own, and has not been independently audited. To reach an agent from outside your network, put it behind a VPN or a tunnel you trust. Read the [threat model](security/threat-model.md) first.

## Options

| Option | Meaning |
|---|---|
| `--listen <address>` | Serve on `ws://`, `tcp://` or `unix://` |
| `--serve` | Serve on standard input and output |
| `--allow-remote` | Permit an address that is not loopback |
| `--authorized-devices <file>` | The device whitelist |
| `--print-identity` | Print the agent's identity and exit |
| `--cwd <directory>` | The workspace |
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
| `/cwd <directory>` | Changes the workspace |
| `/sessions` | Opens the session list |
| `/new` | Starts a session |
| `/name <name>` | Names the session |
| `/abort` | Stops the turn and ends a running tool |
| `/clear` | Clears the visible transcript; the conversation is kept |
| `/help` | Lists commands |

The model, thinking level, workspace and compaction cannot be changed while a turn is running.

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

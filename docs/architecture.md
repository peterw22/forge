# Architecture

Forge has three parts: an agent that runs on the machine you work on, clients that control it, and a relay that delivers notifications.

```text
┌─────────────────────────┐                        ┌──────────────────────────────┐
│ Forge clients           │   authenticated,       │ pi-go-agent                  │
│  iOS, Android           │   encrypted            │                              │
│  macOS, Linux           │◀──────────────────────▶│  sessions ── safety gate     │
│  web                    │   connection           │      │           │           │
└───────────▲─────────────┘                        │  providers     tools         │
            │                                      │      │      shell, files,    │
            │ encrypted                            │      │      browser, schedule│
            │ notification                         └──────┼────────────┬──────────┘
            │                                             │            │
   ┌────────┴────────┐        ciphertext                  ▼            │
   │ Apple, Google   │◀──────────────────┐            model APIs       │
   └─────────────────┘                   │                             │
                                  ┌──────┴───────┐     ciphertext      │
                                  │  push relay  │◀────────────────────┘
                                  └──────────────┘
```

## Repository layout

| Path | Contents |
|---|---|
| `cmd/pi-go-agent` | The agent: sessions, providers, tools, safety gate, authentication, push |
| `cmd/pi-go-tui` | Terminal client |
| `cmd/pi-go-codex` | Provider bridge for the Pi coding agent. See [legacy](legacy-pi-bridge.md) |
| `internal/agent` | The agent loop, independent of any provider |
| `internal/session` | Session files |
| `internal/prompt` | The default system prompt |
| `internal/tui` | Terminal rendering |
| `flutter/pi_go_app` | The Forge client for every platform |
| `worker-push` | The push relay, a Cloudflare Worker |
| `flatpak` | Linux packaging |
| `s3` | Scripts that publish the iOS and web builds |
| `scripts` | Build and maintenance scripts |
| `docs` | This documentation |

## The agent

`pi-go-agent` is one Go program with no runtime dependencies besides the optional browser.

### The loop

`internal/agent` sends the conversation to a provider, receives text and tool calls, runs the tools and repeats until the model stops. It knows nothing about any particular provider or client.

Before each tool runs, the loop asks a guard whether it may. The guard is the [safety gate](security/safety-gate.md).

### Providers

A provider turns a conversation into a stream of events. A router selects one from the model's prefix.

| Provider | Models | Connects through |
|---|---|---|
| [OpenAI Codex](providers/openai-codex.md) | `gpt-…` | WebSocket, with server-sent events as fallback |
| [OpenAI-compatible APIs](providers/openai-compatible.md) | `<name>/…` | Streamed HTTP |
| [Claude Code](providers/claude-code.md) | `claude/…` | The `claude` program |
| [Antigravity](providers/antigravity.md) | `agy/…` | The `agy` program |

The two command line providers execute tool calls inside their own process. The agent disables their built-in tools and serves its own through a local bridge, so those calls pass the same safety gate and appear in the same transcript.

### Tools

| Tool | Does |
|---|---|
| `read` | Reads a file or an image |
| `write` | Writes a whole file |
| `replace` | Replaces one span of a file, matched exactly or by regular expression |
| `bash` | Runs a shell command in its own process group |
| `browser_*` | Drives a headless browser. See [browser](browser.md) |
| `cron` | Schedules a prompt for later |

### Sessions

A session is an append-only file of JSON lines in `<workspace>/.pi-go/sessions/`. It holds the conversation, tool calls and results, summaries, and the conversation ID of a command line provider.

- A session belongs to the agent, not to a client. Closing every client does not stop a running turn, a shell command or a wait for approval.
- Several clients can attach to one session. They see the same stream, and a client that reconnects is caught up.
- Different clients can run different sessions at the same time.
- Starting the agent does not create a session file. The first prompt does.
- Each session works in its own directory, chosen when it starts and kept in its file.

### Compaction

When a conversation grows, `/compact` replaces older turns with a summary. The session file keeps the full history.

## The clients

`flutter/pi_go_app` is one Flutter project that builds for iOS, Android, macOS, Linux and the web.

- It follows the design language of each platform. See the [client README](../flutter/pi_go_app/README.md#platform-look).
- It holds up to two connections at once, to the same agent or to different ones.
- The macOS app can start an agent of its own. Every other client connects to an agent that is already running.
- Identity keys live in the platform's key store. See [device authentication](security/device-authentication.md#device-identity).

Native code handles what Flutter cannot: key storage, and decrypting a notification while the app is not running.

## The relay

`worker-push` is a Cloudflare Worker with a D1 database. It holds the credentials for Apple's and Google's push services, records which device has approved which agent, and forwards encrypted notifications. See [the push relay](security/push-relay.md).

## Protocol

The agent and its clients exchange JSON messages, one per line or WebSocket frame.

| Direction | Examples |
|---|---|
| Client to agent | `prompt`, `abort`, `get_state`, `switch_session`, `new_session`, `list_sessions`, `list_directories`, `set_model`, `approval_response` |
| Agent to client | Responses to commands, and events such as `agent_start`, `message_update`, `tool_execution_start`, `tool_execution_end`, `approval_required`, `agent_end` |

On a network listener every message after the handshake is encrypted. See [device authentication](security/device-authentication.md#session-encryption).

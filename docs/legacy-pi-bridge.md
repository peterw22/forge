# The Pi provider bridge

Forge began as a way to use OpenAI Codex from the Pi coding agent, before it had an agent of its own. That bridge is still in the repository. It is not needed to use Forge.

```text
Pi agent → go-codex extension → pi-go-codex → OpenAI Codex
```

| Part | Path |
|---|---|
| The bridge | `cmd/pi-go-codex` |
| The Pi extension | `extensions/go-codex.ts` |

The extension registers a model provider named `go-codex` in Pi and adapts the bridge's output to Pi's event stream. Pi's own agent loop, tools, sessions and terminal interface work as usual.

## Build and run

```bash
go build -o pi-go-codex ./cmd/pi-go-codex
```

From a checkout of Pi:

```bash
./pi-test.sh -e /path/to/forge/extensions/go-codex.ts
```

Choose a `go-codex` model with `/model`.

| Variable | Meaning |
|---|---|
| `PI_GO_CODEX_BIN` | Path to the bridge, when it is not beside the extension |
| `PI_GO_CODEX_CREDENTIAL_COMMAND` | A command that prints a bearer token, when `pi` does not own your Codex sign-in |

## Protocol

The extension starts the bridge with `--provider` and writes one JSON request to its standard input. The bridge writes JSON lines:

1. `payload`, the request it built. The extension passes it through Pi's hook and returns the replacement.
2. `response`, the HTTP response details when server-sent events are used.
3. `start`, content deltas, then `done` or `error`.

The bridge uses a WebSocket and falls back to server-sent events when the WebSocket fails before any output.

## Limitations

- Each request is a new process, so nothing is reused between requests.
- No request compression on the fallback path.

# Go Codex provider bridge for Pi

This project registers a `go-codex` Pi model provider. Its current catalog is `gpt-5.3-codex-spark`, `gpt-5.5`, `gpt-5.6-luna`, `gpt-5.6-sol`, and `gpt-5.6-terra`. It is a migration bridge, not a replacement for Pi's built-in `openai-codex` provider.

```text
Pi agent core → go-codex provider extension → Go JSONL sidecar → Codex WebSocket/SSE
```

The TypeScript extension adapts the sidecar's JSONL protocol to Pi's `AssistantMessageEventStream`; Pi's existing agent core, tool loop, sessions, and TUI therefore work normally. Select any supported bridge model using `/model`.

Read [MIGRATION-STEP-1.md](MIGRATION-STEP-1.md) before extending the provider bridge. The roadmap for porting the agent loop while reusing Pi's TypeScript TUI is in [GO-AGENT-CORE-PLAN.md](GO-AGENT-CORE-PLAN.md). The dependency policy and stable Go-TUI requirements are in [ARCHITECTURE-AUDIT.md](ARCHITECTURE-AUDIT.md).

## Prerequisites

- Go 1.26 or newer
- A source checkout of Pi with dependencies installed
- An OpenAI Codex login in Pi (`/login` → OpenAI Codex)
- A `pi` executable on `PATH`. The bridge uses `pi auth print-bearer-token --provider openai-codex ...` to obtain an already-refreshed request token.

## Build and test

```bash
cd go
go test ./...
go build -o pi-go-codex ./cmd/pi-go-codex
go build -o pi-go-agent ./cmd/pi-go-agent
go build -o pi-go-tui ./cmd/pi-go-tui
```

The binary is intentionally ignored by Git; rebuild it locally after changing Go code.

## Run with Pi's TUI

From the Pi repository root:

```bash
cd ../pi
./pi-test.sh -e ../go/extensions/go-codex.ts
```

Open `/model`, select a `go-codex` model (for example `go-codex/gpt-5.6-terra`), then send a normal prompt. Pi renders assistant streaming, tool calls, and tool results through its normal interactive TUI because the bridge is a model provider, not an LLM-callable tool.

If the binary is stored elsewhere, set its absolute path before starting Pi:

```bash
PI_GO_CODEX_BIN=/absolute/path/to/pi-go-codex ./pi-test.sh -e ../go/extensions/go-codex.ts
```

If `pi` is not the executable that owns your Codex login, provide an alternate credential command. It must print only a valid bearer token to stdout:

```bash
PI_GO_CODEX_CREDENTIAL_COMMAND='/absolute/path/to/pi auth print-bearer-token --provider openai-codex --model gpt-5.5 --min-expiry 5m' \
  ./pi-test.sh -e ../go/extensions/go-codex.ts
```

## Experimental Go agent core

`pi-go-agent` is the initial Go-owned agent loop. It streams JSONL agent events, uses the Go Codex provider bridge, and executes Go-native `read`, `write`, and `bash` tools:

```bash
./pi-go-agent --model gpt-5.6-terra --prompt "List the repository modules"
```

When `PI_GO_CODEX_TOKEN` is unset, `pi-go-agent` obtains a refreshed short-lived bearer token using `pi auth print-bearer-token`. Set it only when running without the Pi CLI or when supplying an explicit request token.

The binary also exposes the persistent JSONL protocol the TUI adapter will use:

```bash
./pi-go-agent --serve --model gpt-5.6-terra
```

It accepts correlated `prompt`, `abort`, `get_state`, and `shutdown` commands on stdin and emits agent events on stdout. The same JSONL protocol can be served over a local socket:

```bash
# Native TUI/Unix socket
./pi-go-agent --listen "unix://$HOME/.pi-go/agent.sock" --cwd "$PWD"

# Loopback TCP
./pi-go-agent --listen tcp://127.0.0.1:7346 --cwd "$PWD"

# Browser/Flutter WebSocket endpoint
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd "$PWD"
```

TCP and WebSocket listeners are restricted to loopback addresses. WebSocket browser origins must also be loopback. Unix sockets are created with mode `0600`. A health check is available at `http://127.0.0.1:7346/healthz` in WebSocket mode.

The daemon owns tasks independently of client connections. Disconnecting every client does not stop an active provider request, Bash process, compaction, or approval wait. Multiple clients attached to one session receive the same stream and get synthetic in-flight catch-up after reconnecting. Session selection is connection-local: different clients can attach to and run different UUIDv7 sessions concurrently, while clients attached to the same session share its single active task and controls.

## Go terminal frontend

`pi-go-tui` is the Go-native interactive frontend for `pi-go-agent`; it does not require the TypeScript Pi runtime:

```bash
./pi-go-tui --model gpt-5.6-terra
```

It supports streaming text, local ANSI Markdown rendering, live tool output, a fail-closed Bash Safety gate using hardcoded `gpt-5.6-luna` at `low` reasoning with explicit approval dialogs, Ctrl-C tool/process-group abort, runtime session switching with `Ctrl-B` then `s`, `/model <id>`, `/compact [focus]`, `/clear`, and `/exit`. It does not load TypeScript extensions.

The existing TypeScript Pi TUI integration remains on the normal TypeScript backend until the separate `GoAgentSessionAdapter` switch is wired.

## Flutter macOS frontend

The WebSocket-only native **Forge** client (`com.tingouw.forge`) is in `flutter/pi_go_app`. It does not bundle or launch the server. Start the backend separately, then run the app:

```bash
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd "$PWD"
cd flutter/pi_go_app
flutter pub get
flutter run -d macos
```

With full Xcode installed, `./scripts/build-macos-app.sh` creates `dist/macos/Forge.app` and `Forge-macOS.zip`. The macOS connection bar is restricted to WebSocket. The app restores backend state on connect and supports prompts, native GitHub-flavored Markdown, streaming transcript and tool-log updates, abort/process-group kill, model switching, runtime session selection (button or `Ctrl-B` then `s`), a session-specific YOLO switch that bypasses the Luna tool-safety gate, and token usage. Prompt images can be selected with the picker or pasted directly on macOS, web, and Android.

The same Flutter project builds for web:

```bash
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd "$PWD"
cd flutter/pi_go_app
flutter run -d chrome
# or: flutter build web --release
```

## Protocol

The extension starts the Go binary with `--provider` and sends a JSON request on stdin. The sidecar uses Codex Responses WebSocket transport by default and falls back to SSE if the WebSocket fails before streaming begins. Set Pi's model transport to `websocket`, `websocket-cached`, or `sse` to require a specific path; the current one-shot sidecar treats `websocket-cached` as WebSocket without cross-request connection reuse. The Go process emits JSONL events on stdout:

1. `payload` — generated Codex request payload; the extension runs Pi's `onPayload` hook and returns its replacement.
2. `response` — SSE HTTP response metadata; the extension runs Pi's `onResponse` hook. WebSocket streams begin directly with `start`.
3. `start`, content deltas, and `done`/`error` — converted into Pi provider events.

The bearer token is passed only as `PI_GO_CODEX_TOKEN` in the Go child environment. It is not placed in command arguments, JSONL, session files, or tool output.

## Current support

- Codex Responses WebSocket transport with pre-stream SSE fallback
- Pi conversation context: users, assistant text/tool calls, and tool results
- Pi JSON-schema tools and streamed Codex function calls
- output-text deltas, response ID, token usage, completion/length/tool-use stops
- cancellation through Pi's provider abort signal

## Limitations

- No connection-scoped WebSocket continuation cache; each sidecar process opens one provider connection
- No zstd request compression on the SSE fallback path
- Image-aware tool results and direct Flutter user-image attachments are supported; richer reasoning-block rendering remains incomplete
- No provider-specific retry/backoff policy or cost calculation
- OAuth remains owned by Pi's existing `openai-codex` login; native Go OAuth is deferred

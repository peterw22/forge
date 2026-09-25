# Go Codex provider bridge for Pi

This project registers a `go-codex` Pi model provider. Its current catalog is `gpt-5.6-luna`, `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-6-sol`, and `gpt-6-luna`. GPT-5.5 and earlier are no longer advertised. It is a migration bridge, not a replacement for Pi's built-in `openai-codex` provider.

```text
Pi agent core → go-codex provider extension → Go JSONL sidecar → Codex WebSocket/SSE
```

The TypeScript extension adapts the sidecar's JSONL protocol to Pi's `AssistantMessageEventStream`; Pi's existing agent core, tool loop, sessions, and TUI therefore work normally. Select any supported bridge model using `/model`.

Read [MIGRATION-STEP-1.md](MIGRATION-STEP-1.md) before extending the provider bridge. The roadmap for porting the agent loop while reusing Pi's TypeScript TUI is in [GO-AGENT-CORE-PLAN.md](GO-AGENT-CORE-PLAN.md). The dependency policy and stable Go-TUI requirements are in [ARCHITECTURE-AUDIT.md](ARCHITECTURE-AUDIT.md).

## Prerequisites

- Go 1.26 or newer
- A source checkout of Pi with dependencies installed
- An OpenAI Codex login in Pi (`/login` → OpenAI Codex)
- A `pi` executable on `PATH` only for the legacy TypeScript provider bridge; `pi-go-agent` and Forge authenticate natively.

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

For the legacy TypeScript provider bridge only, if `pi` is not the executable that owns your Codex login, provide an alternate credential command. It must print only a valid bearer token to stdout:

```bash
PI_GO_CODEX_CREDENTIAL_COMMAND='/absolute/path/to/pi auth print-bearer-token --provider openai-codex --model gpt-6-sol --min-expiry 5m' \
  ./pi-test.sh -e ../go/extensions/go-codex.ts
```

## Experimental Go agent core

`pi-go-agent` is the initial Go-owned agent loop. It streams JSONL agent events, owns Codex transport directly, and executes Go-native `read`, `write`, and `bash` tools:

```bash
./pi-go-agent --model gpt-5.6-terra --prompt "List the repository modules"
```

When `PI_GO_CODEX_TOKEN` is unset, `pi-go-agent` uses its native OpenAI Codex OAuth device flow. Forge exposes this through its account button. The refresh token is stored at `~/.pi-go/auth.json` with mode `0600`, refreshed automatically, and reused across launches; no Pi installation is required. `PI_GO_CONFIG_DIR` overrides the config directory for managed/test deployments, while `PI_GO_CODEX_TOKEN` remains an explicit non-persistent override. The terminal equivalent is `./pi-go-agent --login`; use `--logout` to remove the stored credential.

Forge's **Providers** dialog also configures Qwen Code Plan independently, including its write-only API key, OpenAI-compatible base URL, Anthropic-compatible base URL, protocol, and model IDs. OpenAI OAuth and Qwen can remain configured simultaneously, and sessions select Qwen models using `qwen-code-plan/<model-id>`. The implemented Qwen transport uses pooled HTTP SSE through the OpenAI-compatible `/chat/completions` API. Forge can query the configured OpenAI-compatible `/models` endpoint and persist the returned model catalog. The Anthropic URL is persisted for forward compatibility, but Anthropic transport is not yet implemented.

`pi-go-agent` owns Codex transport in-process. Each agent session keeps one upstream WebSocket across model tool calls and later user prompts. After the first full request, matching follow-up context is sent as an input delta with the connection-scoped `previous_response_id`. Connections close after five idle minutes or 55 minutes of total age. If a connection or continuation is lost, the agent retries with its locally retained full context; SSE remains the pre-stream transport fallback. Safety classification and compaction use isolated one-shot provider requests so they cannot alter the main session continuation.

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

Server startup without `--resume` uses an in-memory draft session. Connecting a client or changing draft settings does not create a session file or update `.pi-go/latest`; the first prompt promotes the draft using the same session ID. Explicit **New session**/`/new` still creates a persisted session immediately.

Optional mutual P-256 authentication can be enabled for network listeners. In Forge on iOS or Android, open the tuning menu and choose **Copy device whitelist entry**, place that entry inside `~/.pi-go/authorized-devices.json`, then start the server with `--authorized-devices`:

```json
{
  "version": 1,
  "devices": [
    { "name": "Forge phone", "deviceId": "...", "publicKey": { "kty": "EC", "crv": "P-256", "x": "...", "y": "..." }, "fingerprint": "..." }
  ]
}
```

```bash
./pi-go-agent --print-identity
./pi-go-agent --listen ws://192.168.50.50:7346/ws --allow-remote \
  --authorized-devices "$HOME/.pi-go/authorized-devices.json"
```

Before authentication the server sends no session state and accepts no normal commands. On the first connection Forge verifies the server's signed P-256 challenge, displays its fingerprint, and asks the user to trust that identity for the exact WebSocket endpoint. Later identity changes are blocked. Trusted identities can be deliberately removed from Forge's connection manager.

Session-list responses include transient `active` state for running prompts or compactions; Forge renders these sessions with a green dot. The server also broadcasts `session_status` to every connected client when a session starts, becomes idle, or waits for approval. Forge keeps waiting-for-input state in memory, renders it with a higher-priority yellow dot, and issues Android/macOS notifications both when feedback is required and when a session completes. Notifications for the selected session are suppressed while Forge is foregrounded/focused; background and other-session transitions still notify. The daemon owns tasks independently of client connections. Disconnecting every client does not stop an active provider request, Bash process, compaction, or approval wait. Multiple clients attached to one session receive the same stream and get synthetic in-flight catch-up after reconnecting. Session selection is connection-local: different clients can attach to and run different UUIDv7 sessions concurrently, while clients attached to the same session share its single active task and controls.

## Browser tools (headless)

The Go agent provides `browser_navigate`, `browser_click`, `browser_type`, `browser_press_key`, `browser_place_cursor`, `browser_click_cursor`, `browser_scroll`, `browser_screenshot`, and `browser_close` through pinned `playwright-go`. Install the matching driver and Chromium explicitly on the agent host:

```sh
bash scripts/install-browser.sh
FORGE_BROWSER_TEST=1 go test ./cmd/pi-go-agent -run '^TestBrowserHeadlessIntegration$' -v
```

Chromium runs **headless** on the agent machine, with one ephemeral context/page per session. No visible browser window or graphical desktop is required; use the Flutter live view for viewing and manual control. Browser state persists between turns, not daemon restarts; closing the browser, changing workspace, cancelling an active browser operation, or shutting down its runtime discards it. No personal browser profile is used. Browser assets are installed in the user's Playwright caches, not embedded in the Go binary; signed app-bundle/Flatpak browser packaging is not included yet.

Navigation and clicks wait for 500 ms without active page HTTP requests, capped at **10 seconds**, and still attach the current screenshot when that wait expires. WebSockets are not counted. Navigation has a separate 10-second response-commit timeout; a commit timeout also returns a snapshot. `browser_scroll` accepts `direction: "up" | "down"`, moves the main page by 90% of the viewport with overlap, and returns a screenshot with fresh references after the same bounded idle wait. It reports when the page cannot move further; nested scroll panels are not supported. `browser_screenshot` captures the current viewport immediately, without navigation, network-idle or font-loading waits. Screenshot capture/metadata collection still takes processing time and can fail if the page closes. Results include a PNG image plus visible element references such as `e12`; use those with `browser_click`. References are replaced on every snapshot; stale/detached elements fail rather than resolving to another target. Browser batches execute sequentially.

`browser_place_cursor` accepts viewport CSS-pixel `x,y` coordinates (1280×800 by default), moves the pointer without clicking, waits 100 ms for hover effects, and captures the page without waiting for network idle. A red crosshair is composited onto the returned PNG because Chromium page screenshots omit the OS cursor; it does not modify the webpage. `browser_click_cursor` takes `button: "left" | "right"` and clicks the placed pointer without moving it, then uses the same 10-second idle cap and screenshot behavior. Re-place after navigation, scrolling or any click. These tools do not invoke the classifier. Coordinates target the viewport, not a stable element, so page changes between placement and clicking can change the target. Native browser context menus may not appear in page screenshots; webpage-rendered menus do.

`browser_type(text)` types up to 10,000 bytes of literal UTF-8 into the currently focused field; click the field first. `browser_press_key(key)` sends one Playwright key/chord, including Enter, Tab, Shift+Tab, Backspace, Escape and ControlOrMeta+A (Command on macOS, Control elsewhere). To replace text, select all first, then type. Both use the bounded idle wait and screenshot result, without classifier checks, and invalidate cursor placement. Key presses are released within the call; OS/browser-toolbar automation is not supported. Typed text is recorded in tool arguments/transcripts; do not enter secrets. Enter and other keys may submit forms or trigger external effects.

**Safety policy:** only explicit `browser_navigate` URL opens invoke the safety classifier (unless YOLO bypasses it). Clicks, redirects, background requests and screenshots do not request additional approval. Opening a page permits subsequent interaction and may expose its text/images to the model, including sensitive data. This is not a network sandbox or a read-only browser; use trusted test sites. HTTP/HTTPS tool URLs only, no embedded URL credentials, no download persistence, no upload tool, and no supported additional tabs. Popups are closed, though their initial requests may already have happened. Arbitrary JavaScript evaluation is not exposed as an agent tool.

Browser screenshots use the same image content blocks as the `read` tool, so they are sent directly to vision-capable models and shown in the Flutter transcript. OpenAI-compatible chat adapters attach tool images as a following user image message after the complete batch of tool results. Models without vision support cannot interpret them.

## Browser live view and manual control

When a session has an open browser, Flutter shows a browser button on the right side of the transcript. Open it for an on-demand **1280×720 JPEG sequence at up to 2 FPS**, carried over the existing authenticated, AES-GCM-encrypted connection. The 1280×800 browser viewport is scaled and letterboxed, not resized. Pinch to zoom and drag to pan the image; separate arrow buttons scroll the page. The viewer preserves its zoom across frames and keeps only the latest decoded image. Live frames are neither sent to the model nor persisted in session transcripts.

The viewer starts read-only. **Take control** obtains a single-client lease once the current browser action has finished (retry if it is busy). Agent browser tools are blocked while a viewer owns control. Tap to left-click, long-press to right-click; use the text field to send Unicode text into the focused browser field, and the key buttons for Enter, Tab, Backspace, Escape, selection, and arrows. Manual input is acknowledged without being added to chat history. Browser pages can still receive and transmit the typed content, and it can appear in later screenshots. Keyboard input is buffered locally until Send, not forwarded for each IME composition event.

Control and viewing renew every five seconds and expire after fifteen seconds without renewal. Closing/backgrounding the view, disconnecting, switching sessions, or closing the browser releases control. Agent element references and cursor placement are invalidated on takeover and manual input; use a fresh tool screenshot after handoff. Multiple clients can watch, but only one can control. Input includes the browser instance and displayed frame sequence; old-instance, expired-lease, and stale-frame input is rejected. Input coordinates are converted through the zoom/pan and letterbox transforms.

Capture runs independently of the browser action mutex, so agent network-idle waits do not freeze the view. Each viewer has one replaceable pending-frame slot separate from reliable chat/approval responses. Slow clients acknowledge frames after decoding/display; the server allows one in-flight frame per viewer, with a five-second recovery timeout, instead of building a video backlog. Input is checked against frames actually sent to that viewer within the last ten seconds, rather than against the global capture counter. Recoverable input errors do not discard the client's control token. One in-flight encrypted frame can still delay the next reliable response on the same connection. Frames have a 1 MiB JPEG limit and manual input has a bounded execution timeout. Native browser chrome/context menus are not part of page screenshots.

## Go terminal frontend

`pi-go-tui` is the Go-native interactive frontend for `pi-go-agent`; it does not require the TypeScript Pi runtime:

```bash
./pi-go-tui --model gpt-5.6-terra
```

It supports streaming text, local ANSI Markdown rendering, live tool output, a fail-closed Bash Safety gate using a configurable model (default `gpt-5.6-luna`) at `low` reasoning with explicit approval dialogs, Ctrl-C tool/process-group abort, runtime session switching with `Ctrl-B` then `s`, `/model <id>`, `/compact [focus]`, `/clear`, and `/exit`. It does not load TypeScript extensions.

The existing TypeScript Pi TUI integration remains on the normal TypeScript backend until the separate `GoAgentSessionAdapter` switch is wired.

## Flutter macOS frontend

The native **Forge** client (`com.tingouw.forge`) is in `flutter/pi_go_app`. The packaged macOS app includes a universal `pi-go-agent` helper and offers a **Local** connection that starts it with the user's home directory as its initial workspace. Local disconnect shows a warning and terminates only that bundled helper; remote WebSocket servers are never stopped. Android and web remain remote-only. During Flutter development, start a backend separately:

```bash
./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd "$PWD"
cd flutter/pi_go_app
flutter pub get
flutter run -d macos
```

With full Xcode installed, `./scripts/build-macos-app.sh` creates `dist/macos/Forge.app` and `Forge-macOS.zip`. The script automatically discovers signing identities in Keychain in this order: `Developer ID Application`, `Apple Development`, then ad-hoc. Override discovery with `SIGN_IDENTITY="certificate name" ./scripts/build-macos-app.sh`. On this development machine the discovered identity is `Apple Development: Tingou Wu (KUB5DTDMMS)`. Apple Development signing identifies a local/test build but is **not** accepted by Gatekeeper as direct-download distribution signing on another Mac; public distribution requires a `Developer ID Application` certificate and Apple notarization.

**Gatekeeper** is macOS's download security check. When an app carries the quarantine attribute (for example after a browser download), Gatekeeper verifies that it is signed by a trusted Developer ID, has not been modified, and normally has an Apple notarization ticket. Ad-hoc or Apple Development signatures can run on the developer's own configured Mac, but recipients may see “unidentified developer,” “cannot be opened,” or need a manual override. `codesign --verify` checks signature integrity only; `spctl --assess --type execute` tests Gatekeeper acceptance.

After installing a Developer ID certificate, build and inspect with:

```bash
./scripts/build-macos-app.sh
codesign -dv --verbose=4 dist/macos/Forge.app
spctl --assess --type execute --verbose=2 dist/macos/Forge.app
```

The app should also be notarized and stapled before public download distribution. The macOS connection bar supports the bundled Local runtime and remote WebSocket servers. The app restores backend state on connect and supports prompts, native GitHub-flavored Markdown, streaming transcript and tool-log updates, abort/process-group kill, model switching, runtime session selection (button or `Ctrl-B` then `s`), a session-specific YOLO switch that bypasses the configured classifier safety gate, and token usage. Assistant reasoning summaries stream into an expandable Thinking panel. Prompt images can be selected with the picker or pasted directly on macOS, web, and Android. Forge retains up to two simultaneous backend connections, which may target the same server or different servers. The badged network button opens a manager for switching, adding, and closing connections; each connection keeps independent session and transcript state. On Android, encrypted FCM notifications remain available when the WebSocket is suspended; tapping one reopens Forge and reconnects. The settings menu changes the persistent safety-classifier model (default `gpt-5.6-luna`) independently from the conversation model. The classifier must generate a privacy-bounded one-sentence summary for blocked operations and completed assistant turns. Safety decisions (including their notification summaries) and completion summaries are submitted through output-only tools with individual typed arguments, using automatic tool selection while thinking remains enabled. Plain response text is never parsed as a result; missing, malformed, duplicate, or incomplete tool submissions retain the existing manual-approval or fixed-summary fallback. Tool schemas guide generation, and Go validates the fields and notification constraints locally. Approval/completion pushes carry that summary only inside the existing AES-GCM envelope. Session pickers show completion time plus the latest persisted classifier summary, never the UUID or raw transcript preview.

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

Provider credentials are stored only in the versioned `~/.pi-go/auth.json` (mode `0600`, parent directory `0700`) and are not placed in command arguments, JSONL session files, protocol status, or tool output. Existing flat OpenAI OAuth credentials migrate without logout. `PI_GO_CODEX_TOKEN` and `PI_GO_QWEN_API_KEY` remain optional process-environment overrides.

## Current support

- Codex Responses WebSocket transport with pre-stream SSE fallback
- Pi conversation context: users, assistant text/tool calls, and tool results
- Pi JSON-schema tools and streamed Codex function calls
- output-text deltas, response ID, token usage, completion/length/tool-use stops
- cancellation through Pi's provider abort signal

## Limitations

- The TypeScript provider-extension sidecar remains one-shot and has no connection-scoped continuation cache; the Go agent owns persistent session connections directly
- No zstd request compression on the SSE fallback path
- Flutter renders provider reasoning summaries; encrypted or hidden raw chain-of-thought is not exposed
- No provider-specific retry/backoff policy or cost calculation
- OAuth device login and refresh are native to `pi-go-agent`; credentials are stored in a local owner-only JSON file rather than the OS keychain

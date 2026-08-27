# Qwen Code Plan Provider Implementation Plan

## Goal

Add a first-class `qwen-code-plan` model provider to `pi-go-agent` and Forge without changing the provider-neutral agent loop.

The provider will use Qwen Code Plan's HTTP streaming APIs rather than the OpenAI Codex Responses WebSocket protocol. Its API key and configurable base URLs will be persisted in the same owner-only configuration file currently used for OpenAI OAuth:

```text
~/.pi-go/auth.json
```

Default service base URLs:

| Protocol | Default base URL |
| --- | --- |
| OpenAI-compatible | `https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1` |
| Anthropic-compatible | `https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic` |

The OpenAI-compatible protocol should be implemented first. Anthropic compatibility should be represented in configuration from the beginning but added as a second transport adapter after the OpenAI path is stable.

OpenAI Codex OAuth and Qwen Code Plan configuration are independent and may be active simultaneously. Configuring Qwen must not sign the user out of OpenAI, and signing in or out of OpenAI must not modify Qwen's API key or endpoints. Provider selection belongs to each session/model, not to one global authentication mode.

## Important distinction

Qwen Code Plan is not an OpenAI Codex Responses service.

It must **not** reuse the Codex-specific assumptions in `codex_provider.go`:

- no ChatGPT OAuth JWT or `ChatGPT-Account-ID` header;
- no `/responses` Codex payload;
- no `OpenAI-Beta` header;
- no Codex Responses WebSocket;
- no `previous_response_id` continuation cache;
- no encrypted Codex reasoning items.

The initial Qwen transport will use HTTP POST plus server-sent streaming from an OpenAI-compatible chat-completions endpoint. Each model turn will include the retained conversation context.

## Proposed user-facing model identity

Use canonical provider-qualified model references:

```text
openai-codex/gpt-5.6-terra
qwen-code-plan/<configured-model-id>
```

Do not infer a provider solely from a model-name prefix. A user-configured Qwen deployment could expose arbitrary model IDs.

For compatibility, existing unqualified GPT IDs can remain aliases for `openai-codex/<id>` during migration. New sessions should persist canonical provider-qualified references.

Qwen model IDs should be user-configurable because plan availability and model names may vary by account and region. Forge should not ship an unverified fixed Qwen catalog. A configuration can optionally contain a default model and an ordered list of models.

## Configuration format and migration

### Versioned shared file

Migrate the current flat OpenAI credential object to a versioned provider map. The resulting `auth.json` should conceptually look like this:

```json
{
  "version": 2,
  "providers": {
    "openai-codex": {
      "type": "oauth",
      "access_token": "<secret>",
      "refresh_token": "<secret>",
      "expires_at": 0,
      "account_id": "<account>"
    },
    "qwen-code-plan": {
      "type": "api_key",
      "api_key": "<secret>",
      "protocol": "openai",
      "openai_base_url": "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
      "anthropic_base_url": "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic",
      "default_model": "<user-selected-model-id>",
      "models": [
        "<user-selected-model-id>"
      ]
    }
  }
}
```

Field names in the final Go structs may differ, but the following properties are required:

- a schema version;
- separate provider records;
- API key and both base URLs persisted together;
- selected protocol persisted as `openai` or `anthropic`;
- configurable model list and default model;
- no secrets in session JSONL files.

### Migration behavior

On first read of the current flat OAuth shape:

1. Decode it as the existing `codexCredential`.
2. Convert it in memory to `providers["openai-codex"]`.
3. Preserve every token and expiry field exactly.
4. Atomically write version 2 only after successful validation.
5. Keep a test fixture for the old format so upgrades cannot log users out.

A failed migration must leave the original file untouched.

### File guarantees

Refactor `codexAuthManager` into a provider configuration store, for example:

```go
type ProviderConfigStore interface {
    OpenAICodex(context.Context) (CodexCredential, error)
    QwenCodePlan(context.Context) (QwenConfig, error)
    UpdateQwen(context.Context, QwenConfig) error
    DeleteProvider(context.Context, string) error
}
```

The store must:

- create `~/.pi-go` as `0700`;
- write `auth.json` as `0600`;
- serialize updates with one process-wide mutex;
- use write-to-temporary-file plus `rename`;
- preserve unrelated provider records during an update;
- never log or return API keys in status responses;
- redact secrets from HTTP errors and diagnostics.

`PI_GO_CONFIG_DIR` should continue to relocate this file for tests and managed installations.

## Security constraints

Storing the API key in a file is convenient, but mode `0600` only protects it from other operating-system users. The coding agent and Bash tools run as the same user and start in the user's home directory, so they could technically access `~/.pi-go/auth.json`.

The implementation should therefore add defense in depth:

- mark `.pi-go/auth.json` as an unconditional protected path in built-in `read` and `write` tools;
- classify shell access to `.pi-go/auth.json` as credential access and require explicit approval;
- never include the API key in model context, tool output, protocol status, or UI state;
- never put the API key in process arguments;
- document that YOLO mode and arbitrary same-user shell commands cannot provide a hard security boundary around a plaintext credential file.

A later macOS-specific enhancement may move secrets to Keychain while retaining non-secret provider settings in `auth.json`, but that is outside the requested file-based first implementation.

### Remote Forge warning

The current remotely exposed WebSocket server has no transport authentication and may use plain `ws://`. Do not allow a remote Android/WebSocket client to submit or retrieve provider secrets in the first release.

Initially:

- local macOS process transport may edit the Qwen API key;
- CLI configuration may edit the Qwen API key on the server host;
- remote clients may receive only redacted provider status and model metadata.

Remote secret administration should wait for authenticated TLS transport or a one-time pairing protocol.

## Provider architecture

### Provider router

Keep `internal/agent.Provider` unchanged and introduce a router in `cmd/pi-go-agent`:

```go
type providerRouter struct {
    codex agent.Provider
    qwen  agent.Provider
}

func (r *providerRouter) Stream(ctx context.Context, req agent.Request) (
    <-chan agent.ProviderEvent,
    <-chan error,
)
```

The router will parse the canonical model reference, remove the provider prefix before sending the upstream model ID, and delegate to the selected provider.

It must also implement `agent.ProviderSessionCloser`:

- forward `CloseSession` to Codex, which owns persistent WebSockets;
- Qwen has no connection-scoped session state to close;
- reject unknown providers before any network request.

This keeps agent turns, tools, compaction, sessions, and frontends provider-neutral.

### Safety-provider separation

`newSafetyGate(provider)` currently sends a hardcoded `gpt-5.6-luna` request through the same provider object. Adding routing makes this behavior ambiguous and could force Qwen-only users to have OpenAI credentials.

Separate the safety provider from the conversation provider:

```go
type SafetyConfig struct {
    Provider string
    Model    string
}
```

Initial safe behavior should be one of:

1. use OpenAI Codex Luna when OpenAI OAuth is configured;
2. use an explicitly selected Qwen safety model when configured and tested;
3. otherwise fail closed and request manual approval for guarded operations.

Never silently bypass Bash Safety because one provider is not configured. Compaction can continue through the active conversation provider and model.

## Qwen OpenAI-compatible transport

### Endpoint construction

Treat the configured value as a base URL. Normalize one trailing slash and append:

```text
/chat/completions
```

Default result:

```text
https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1/chat/completions
```

Validation rules:

- require absolute `https://` by default;
- allow `http://localhost` and loopback only for development;
- reject URL user-info, fragments, and unexpected query strings;
- show the exact derived endpoint in the configuration test dialog;
- apply connect, response-header, and stream-idle timeouts.

### Authentication

Use:

```http
Authorization: Bearer <qwen-api-key>
Content-Type: application/json
Accept: text/event-stream
```

The API key is read immediately before the request so configuration changes do not require restarting the daemon.

### Request shape

The provider should build a standard streaming chat-completions request:

```json
{
  "model": "<upstream-model-id>",
  "stream": true,
  "stream_options": { "include_usage": true },
  "messages": [],
  "tools": [],
  "tool_choice": "auto",
  "parallel_tool_calls": true
}
```

Only send optional fields after confirming that the service accepts them. In particular, `stream_options` and `parallel_tool_calls` may need capability flags or a compatibility retry without unsupported fields.

### Message conversion

Map `agent.Message` to OpenAI chat messages as follows:

| Agent message | OpenAI-compatible message |
| --- | --- |
| system prompt | `role: system` |
| user text | `role: user`, text content |
| user image | multimodal `image_url` data URL when the selected model supports vision |
| compaction summary | user message with the existing compaction preamble |
| assistant text | `role: assistant`, `content` |
| assistant tool call | `role: assistant`, `tool_calls[]` |
| tool result | `role: tool`, `tool_call_id`, serialized textual content |

An assistant message containing both text and tool calls must remain one assistant message. Do not split it into multiple messages in a way that breaks tool-call ordering.

For tool-result images, start with a textual placeholder unless the Qwen endpoint's tool-role multimodal behavior is verified.

### Tool schema conversion

Map each `agent.Tool` to:

```json
{
  "type": "function",
  "function": {
    "name": "read",
    "description": "...",
    "parameters": {}
  }
}
```

Preserve JSON Schema without adding provider-specific properties. Continue validating emitted calls in the agent loop.

### Streaming parser

Parse SSE incrementally with a larger scanner/read buffer. Support:

- `data: {json}` records;
- multi-line SSE `data:` fields;
- terminal `data: [DONE]`;
- `choices[].delta.content` text deltas;
- optional `choices[].delta.reasoning_content` reasoning deltas when present;
- incremental `choices[].delta.tool_calls[]` keyed by tool-call index;
- fragmented function names and JSON argument strings;
- `choices[].finish_reason`;
- final or streamed `usage` fields;
- structured API errors returned before streaming;
- cancellation through the request context.

Accumulate each tool call until its arguments form valid JSON, then emit one `agent.ProviderToolCall`. Emit:

```text
ProviderTransport: SSE
ProviderTextDelta
ProviderThinkingDelta (only when supplied)
ProviderToolCall
ProviderDone
```

Normalize finish reasons:

| Upstream | Agent |
| --- | --- |
| `stop` | `stop` |
| `tool_calls` / `function_call` | `toolUse` |
| `length` | `length` |
| content filter or unknown terminal error | descriptive error |

If usage is omitted, return zero usage rather than inventing exact values. The agent may retain its current local context estimate.

### HTTP efficiency

Qwen will not have Codex's connection-scoped response continuation. Efficiency should come from standard HTTP behavior:

- reuse one tuned `http.Transport` and connection pool;
- keep HTTP/2 enabled;
- stream responses rather than buffering them;
- reuse TLS connections;
- rely on existing compaction to bound full-context requests;
- avoid retrying after any output or tool-call delta has been emitted;
- retry selected `429`, `502`, `503`, and `504` responses only before streaming, honoring `Retry-After`;
- cap retry count and apply jittered exponential backoff;
- expose upstream transport as `SSE` or `HTTP-SSE`, not `WS`.

Do not emulate `previous_response_id`; the chat-completions service has no equivalent contract.

## Anthropic-compatible adapter

Implement this after the OpenAI-compatible adapter passes integration tests.

Treat the configured Anthropic URL as a base URL and determine the documented derived messages endpoint during implementation. Do not assume whether the supplied `/apps/anthropic` value expects `/v1/messages` to be appended until verified against the service.

The adapter will need:

- Anthropic-style API-key/version headers required by the plan service;
- system prompt outside the message list;
- Anthropic content blocks for text, images, tool use, and tool results;
- event-stream parsing for message/content-block start, delta, and stop events;
- incremental `input_json_delta` tool arguments;
- Anthropic usage mapping.

Both adapters should emit the same `agent.ProviderEvent` contract, allowing protocol selection without changing the agent loop.

## Runtime protocol additions

Replace OpenAI-specific auth commands with provider-oriented configuration commands while retaining aliases during migration:

```text
list_providers
get_provider_config
set_provider_config
remove_provider_config
test_provider_config
list_models
```

Rules:

- `get_provider_config` returns `apiKeyConfigured: true/false`, never the key;
- `set_provider_config` accepts a write-only API key field;
- an omitted API key preserves the stored key;
- an explicit `clearApiKey` removes it;
- `test_provider_config` performs a minimal authenticated request and returns redacted diagnostics;
- model lists include provider, model ID, display label, configured protocol, and capabilities.

Existing `auth_status`, `auth_login`, and `auth_logout` can remain OpenAI aliases until Forge migrates to the provider-oriented API.

## Forge UI

### Provider settings

Turn the current OpenAI account control into a **Providers** settings dialog with two independent cards or tabs:

1. **OpenAI Codex**
   - signed-in state;
   - OAuth login/logout controls;
   - login remains usable while Qwen is configured.
2. **Qwen Code Plan**
   - enabled/configured state;
   - obscured write-only API-key field;
   - protocol selector: OpenAI-compatible or Anthropic-compatible;
   - OpenAI base URL;
   - Anthropic base URL;
   - model list editor;
   - default model selector;
   - Restore defaults action;
   - Test connection action;
   - configuration remains usable while OpenAI OAuth is signed in.

Both cards can show `configured` at the same time. There is no mutually exclusive provider-login toggle. Saving, clearing, signing in, or signing out affects only the selected provider record. Do not repopulate the API-key field from the server; display only `API key configured`.

### Model selector

Remove the fixed Flutter `models` constant as the authoritative catalog. Populate model choices from `list_models` and retain command completion from that runtime list.

Display qualified labels from every currently configured provider at the same time, for example:

```text
OpenAI Codex · gpt-5.6-terra
Qwen Code Plan · <model-id>
```

A user may keep one session on OpenAI and another on Qwen concurrently. Changing a session's model/provider must not alter either provider's credentials. The protocol should remain a provider setting, not part of the model ID.

### Sessions

Persist the canonical provider/model reference in each session. Existing session files with bare GPT model IDs should resolve through the compatibility alias. Switching a session should restore its provider as well as model and thinking level.

## CLI configuration

Add non-secret status commands and an interactive/write-only setup path. Avoid API keys in command-line arguments because they appear in shell history and process listings.

Preferred flow:

```text
pi-go-agent --configure-provider qwen-code-plan
```

The command should read the API key without echo, prompt for base URLs/protocol/models, validate, and atomically save it.

Environment overrides may be supported for CI without persistence:

```text
PI_GO_QWEN_API_KEY
PI_GO_QWEN_OPENAI_BASE_URL
PI_GO_QWEN_ANTHROPIC_BASE_URL
PI_GO_QWEN_PROTOCOL
```

Environment secrets override stored values but must never be copied into `auth.json` automatically.

## Error handling

Errors shown to users should identify:

- provider and model;
- derived endpoint host/path without credentials;
- HTTP status;
- request ID headers when available;
- whether failure occurred before or after streaming;
- authentication/configuration remediation.

Never include:

- API key;
- full Authorization header;
- complete request body containing user context;
- credential-file contents.

A `401` or `403` should report that the Qwen API key or plan entitlement must be checked. It must not trigger OpenAI OAuth login.

## Test plan

### Configuration store

- migrate existing flat OpenAI OAuth credential without data loss;
- read/write version 2 with both providers;
- preserve OpenAI fields when updating Qwen;
- preserve Qwen fields when refreshing OpenAI OAuth;
- enforce directory `0700` and file `0600`;
- survive interrupted temporary writes;
- reject malformed URLs and protocols;
- confirm status responses never contain API keys.

### OpenAI-compatible request conversion

- system, user, assistant, and compaction messages;
- mixed assistant text plus multiple tool calls;
- tool results and error results;
- image inputs;
- JSON Schema tools;
- canonical provider prefix removal.

### SSE parser

- fragmented text;
- fragmented reasoning content;
- interleaved multiple tool calls by index;
- fragmented JSON arguments;
- `[DONE]` with and without usage;
- non-2xx JSON error;
- malformed event;
- stream ending before a finish reason;
- cancellation;
- no retry after first emitted delta.

### Router and sessions

- qualified model routes to the correct provider;
- bare legacy GPT model routes to Codex;
- unknown provider is rejected locally;
- Qwen session does not create a Codex WebSocket;
- provider/model survives persist and resume;
- switching between OpenAI and Qwen closes obsolete Codex session state;
- compaction uses the active provider;
- safety remains fail-closed when its configured provider is unavailable.

### Integration

Use `httptest.Server` for deterministic chat-completions streams and tool calls. Keep real Qwen tests opt-in behind environment variables so CI never requires or exposes a paid API key.

### Flutter

- provider settings render redacted status;
- API-key field is write-only;
- defaults restore correctly;
- runtime model list drives picker and `/model` completion;
- Android cannot administer secrets over an unauthenticated remote connection;
- model/provider state updates after session switching.

## Delivery phases

### Phase 1: configuration foundation

- introduce versioned provider config store;
- migrate current OpenAI OAuth record;
- add Qwen settings and redacted status protocol;
- add protected credential-path checks;
- add local CLI configuration.

### Phase 2: OpenAI-compatible Qwen provider

- implement request conversion;
- implement HTTP/SSE client and tool-call parser;
- introduce provider router and canonical model references;
- persist provider-qualified models in sessions;
- add unit and `httptest` coverage.

### Phase 3: Forge integration

- provider settings dialog;
- local write-only API-key setup;
- dynamic model catalog;
- connection test and clear error messages;
- preserve Android's remote-only/local-option restrictions.

### Phase 4: Anthropic-compatible transport

- verify exact endpoint/header contract;
- implement Anthropic request and event conversion;
- expose protocol switch;
- add parity tests against the OpenAI adapter.

### Phase 5: hardening

- authenticated/TLS remote provider administration or pairing;
- rate-limit/retry tuning from observed service behavior;
- optional Keychain-backed secrets on macOS;
- opt-in real-service smoke tests;
- telemetry-free request diagnostics and redaction audit.

## Acceptance criteria

The provider is complete when:

1. A user can configure the Qwen API key, protocol, both base URLs, and model IDs without editing source.
2. Configuration persists in `~/.pi-go/auth.json` without deleting OpenAI OAuth credentials.
3. Forge can select a Qwen model and complete multi-turn tool loops.
4. Qwen requests use only its API key and never depend on Pi or OpenAI OAuth.
5. OpenAI Codex sessions continue using their persistent WebSocket behavior unchanged.
6. Qwen uses pooled HTTP SSE and sends bounded full context after compaction.
7. Existing sessions and flat OAuth files migrate without logout or data loss.
8. API keys never appear in protocol status, logs, session files, process arguments, or UI state returned from the server.
9. Safety remains fail-closed if the configured safety provider is unavailable.
10. Unit, protocol, Flutter, migration, and opt-in integration tests pass on macOS and Android builds.
11. OpenAI OAuth and Qwen API-key configuration can coexist, and simultaneous sessions can use different providers without changing shared credentials.

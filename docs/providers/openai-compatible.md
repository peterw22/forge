# OpenAI-compatible APIs

Forge can use any service that implements the OpenAI chat completions API with streaming, authenticated by an API key. You can configure several, each under its own name. Models appear as `<provider-name>/<model-id>`.

## Setup

In Forge, open **Providers** and choose **Add API provider**.

| Field | Notes |
|---|---|
| Provider name | 1 to 48 letters, digits, hyphens or underscores. It becomes the prefix of the provider's models |
| API key | Write-only. Forge shows only whether a key is stored |
| Protocol | OpenAI-compatible |
| Base URL | `https://`, or `http://` for a loopback address. No credentials, query or fragment |
| Default model, model list | The model IDs the service offers |

**Fetch models** reads the list from the service's `/models` endpoint.

Providers are independent. Configuring one does not affect another, and does not sign you out of OpenAI Codex. Different sessions can use different providers at the same time.

## Transport

Requests go to `<base URL>/chat/completions` as streamed HTTP. Connections are pooled and reused.

Unlike OpenAI Codex, the service keeps no conversation state, so each request carries the conversation so far. Compaction keeps that bounded.

## Images

Images in prompts are sent as image content. Images returned by a tool, such as a browser screenshot, are attached in a user message that follows the batch of tool results. A model without vision cannot interpret them.

## Credentials

Provider credentials are stored in `~/.pi-go/auth.json`, or under `PI_GO_CONFIG_DIR`, with mode `0600` in a `0700` directory.

- The key is never returned to a client, placed in command line arguments, written to a session file or shown in tool output.
- The file tools refuse to read or write that file, including through a symlink.
- Over a network, a key is only ever sent on an authenticated, encrypted connection.

The file protects the key from other users of the machine. The agent's shell commands run as the same user, so the [safety gate](../security/safety-gate.md) is what stands between a command and that file. See the [threat model](../security/threat-model.md).

## Environment overrides

For a provider named `qwen-code-plan`, these variables override the stored values without saving them:

| Variable | Overrides |
|---|---|
| `PI_GO_QWEN_API_KEY` | API key |
| `PI_GO_QWEN_OPENAI_BASE_URL` | OpenAI-compatible base URL |
| `PI_GO_QWEN_ANTHROPIC_BASE_URL` | Anthropic-compatible base URL |
| `PI_GO_QWEN_PROTOCOL` | Protocol |

## Not implemented

- **The Anthropic-compatible protocol.** Its base URL is stored for later use; selecting it does not work yet.
- Retries and backoff specific to a provider.
- Cost calculation.

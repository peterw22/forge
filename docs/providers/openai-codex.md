# OpenAI Codex

Forge can use OpenAI's Codex models with a ChatGPT account. This is the default provider.

## Signing in

In Forge, open **Providers** and choose **Sign in** under OpenAI Codex. Forge shows a code and a link; enter the code on OpenAI's page.

From a terminal on the agent's machine:

```bash
./pi-go-agent --login
./pi-go-agent --logout
```

The refresh token is stored in `~/.pi-go/auth.json`, or under `PI_GO_CONFIG_DIR`, with mode `0600`. It is refreshed automatically and reused across restarts.

`PI_GO_CODEX_TOKEN` supplies a token for one process without storing it.

## Transport

Each session keeps one WebSocket to the service across tool calls and later prompts.

- After the first request, a follow-up sends only what is new and refers to the previous response.
- A connection closes after five idle minutes, or 55 minutes in total.
- If the connection or the continuation is lost, the agent retries with the full conversation, which it keeps locally.
- If the WebSocket fails before any output, the agent falls back to server-sent events.

The safety classifier and compaction use separate one-shot requests, so they cannot disturb a session's continuation.

Forge shows the transport in use as `upstream` in the header.

## Thinking

Reasoning summaries stream into the Thinking panel. Hidden or encrypted reasoning is not shown.

## Limitations

- No request compression on the fallback path.
- No retries or backoff specific to the provider.
- No cost calculation.

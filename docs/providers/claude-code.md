# Claude Code

Forge can use Claude through the Claude Code command line program, signed in with your own account. Models appear as `claude/<model-id>`.

Verified with Claude Code 2.1.282.

## Setup

Install Claude Code on the machine that runs `pi-go-agent` and sign in:

```bash
claude auth login
```

Then refresh the model list in Forge. The **Providers** screen shows whether Claude Code is installed and signed in. When it is absent, the other providers are unaffected.

## Models

The list comes from Claude Code itself and contains every model it offers the signed-in account.

- Discovery sends no prompt and makes no model call. It runs in a private, empty directory with no tools, servers or settings, and the result is cached for five minutes.
- Aliases such as `default` and `opus` are listed once, under the pinned ID they resolve to. A session therefore selects a fixed model version.
- Only IDs of the form `claude-…` are accepted, because the ID is passed to `--model`.
- If discovery fails, a pinned fallback list is offered and discovery is retried after 30 seconds.

Thinking levels `low`, `medium`, `high`, `xhigh` and `max` are passed to `--effort`. Each model's supported levels are enforced. `off` and `minimal` are rejected for Claude.

## How a turn runs

Claude Code normally executes tools itself. Forge turns that off and supplies its own tools, so that every call passes the [safety gate](../security/safety-gate.md).

```text
pi-go-agent ──▶ claude --print ──▶ local bridge ──▶ safety gate ──▶ tool
                 (no tools of          (authenticates
                  its own)              each call)
```

Each turn starts Claude with:

```text
--print --output-format stream-json --verbose --include-partial-messages
--input-format stream-json
--tools '' --restricted --strict-mcp-config --mcp-config <one server>
--setting-sources '' --permission-mode dontAsk
--allowedTools 'mcp__pi-go-agent__*'
--append-system-prompt <guidance> --disable-slash-commands
```

- Claude's built-in tools and any inherited servers are disabled.
- The only server is the agent's own binary, which forwards each call to a bridge on the loopback interface. The bridge requires a token created for that turn.
- Before it forwards any text, the agent checks the tool inventory Claude reports and stops if it contains anything else.
- The guidance in the system prompt is a usability hint. Enforcement comes from the disabled tools and the bridge.

The bridge exposes `read`, `write`, `replace` and `bash`. Browser and schedule tools are not available to Claude.

When the safety gate refuses a call, the agent asks for approval in Forge, as it does for its other providers.

There is no automatic time limit on a turn. Stopping a turn in Forge ends the Claude process and any tool that is running.

## Sessions

- Later turns resume Claude's conversation by its ID with `--resume`, never `--continue`, so concurrent sessions cannot pick up each other's conversation.
- The ID is saved in the session file as soon as Claude reports it. A turn that is stopped or fails can therefore still be resumed.
- A session restores only a conversation that matches its model and workspace. Changing either starts a new conversation.
- If a session has assistant history and no saved conversation, the agent reports an error and does not silently start over.

## Input

Text and images (PNG, JPEG, GIF, WebP) are sent on standard input, in order, including a prompt that contains only images. Image data is never placed in command line arguments. Other attachments are rejected.

## Transcript order

Claude may write commentary, call a tool, then write its answer. The agent ends the assistant message at each tool call, so a reloaded session shows commentary, tool and answer in the order they happened.

## Thinking

When Claude sends thinking text, Forge shows it in the Thinking panel. Claude Code may send only token counts; the agent does not invent a summary from them.

## Stopping a response

When you stop a response while a tool is running, Claude is ended before it receives that tool's result. Forge still shows the result, because the agent ran the tool. On the next turn Claude knows only that the call was interrupted.

## Claude as the safety classifier

The classifier model can be a `claude/…` model. Each decision is a separate run:

```text
claude --print --output-format stream-json --verbose
  --json-schema <schema> --system-prompt <classifier instructions>
  --tools '' --restricted --strict-mcp-config --mcp-config '{"mcpServers":{}}'
  --setting-sources '' --permission-mode dontAsk
  --no-session-persistence --disable-slash-commands
```

- The operation is sent on standard input, so it does not appear in the process list.
- The run uses a private, empty directory, never the workspace. A `CLAUDE.md` in the workspace cannot influence the gate.
- The agent requires that Claude reports no servers and no tool other than the one for structured output.
- The result passes the same validation as for other providers. Any failure means approval is required.

## Compaction

`/compact` asks Claude Code to compact its own conversation, because summarizing the agent's copy would not shrink what Claude resumes.

- Claude keeps the same conversation ID.
- The agent requires the compaction marker, the summary and a successful result, and no tools. Otherwise the compaction fails.
- The summary and the token counts before and after are recorded. The session history is kept.

## YOLO mode

YOLO skips the safety gate for the session. The disabled built-in tools, the bridge's authentication and the inventory check remain.

# Antigravity (Gemini)

Forge can use Gemini through Google's Antigravity command line program, `agy`. Models appear as `agy/<model-id>`.

> **Supported but not fully tested.** The integration works in the cases described here, with agy 1.2.11. It has had far less use than the other providers, so expect rough edges.

## Setup

Forge gives `agy` its own profile, so your normal Antigravity profile is left untouched.

| | Location |
|---|---|
| Default | `~/.pi-go/agy` |
| With `PI_GO_CONFIG_DIR` | `$PI_GO_CONFIG_DIR/agy` |
| Override | `PI_GO_AGY_HOME` |

Install `agy` on the machine that runs `pi-go-agent`, then sign in once inside that profile:

```bash
HOME="$HOME/.pi-go/agy" agy
```

Then refresh the model list in Forge. When `agy` is missing or signed out, the other providers are unaffected and Forge shows the command to run.

## How a turn runs

`agy` normally executes tools itself. Forge denies those and supplies its own tools, so that every call passes the [safety gate](../security/safety-gate.md).

Each turn runs:

```text
agy -p --output-format stream-json --model <id> --disable-slash-commands
```

The dedicated profile contains a permission policy that allows only Forge's tools:

```json
{
  "permissions": {
    "allow": ["mcp(pi-go-agent/*)"],
    "deny": ["read_file(*)", "write_file(*)", "read_url(*)", "execute_url(*)", "command(*)", "unsandboxed(*)"]
  }
}
```

The exact policy is in [`antigravity-policy.json`](antigravity-policy.json).

- The agent verifies the policy before each tool call and refuses to continue if it has changed.
- The profile's server configuration holds no path. It starts whatever binary the agent names for that turn, so rebuilding or moving the agent needs no change.
- Servers configured in the workspace or inherited from elsewhere are blocked.
- `--dangerously-skip-permissions` is never used.
- The agent never overwrites a configuration file it did not write.

The bridge exposes `read`, `write`, `replace` and `bash`. Browser and schedule tools are not available to `agy`.

When the safety gate refuses a call, the agent asks for approval in Forge.

`agy` has no option for a system prompt, so the agent places its guidance at the start of the first prompt. That is a usability hint; enforcement comes from the policy and the bridge.

## Sessions

- The agent takes the conversation ID from `agy` and resumes with `--conversation <id>`, never `--continue`, so concurrent sessions stay separate.
- The ID is stored in the session file with the transcript and tool calls.
- A session restores only a conversation that matches its model and workspace.
- If a session has assistant history and no saved conversation, the agent reports an error.

## Models

Models are listed with `agy models`. The command is bounded in time and output, invalid IDs are dropped, and a failure does not affect the other providers.

## Thinking

`agy` reports the number of thinking tokens and usually no thinking text. Forge shows thinking only when `agy` sends it.

## Compaction

`agy` cannot compact a conversation. `/compact` reports an error for these sessions. Start a new session to reduce context.

## Gemini as the safety classifier

The classifier model can be an `agy/…` model. `agy` has no option for a schema or a system prompt, so each decision is a new conversation whose prompt contains the instructions, the output schema and the operation.

- The reply must be exactly one JSON object, optionally in one code fence. It passes the same validation as for other providers, and any failure means approval is required.
- The run uses a private, empty directory and is given no bridge credentials, so it has no usable tools.
- The conversation is deleted afterwards. Symlinks are not followed.
- The prompt is passed as an argument, because `agy` cannot read one from standard input. It is therefore visible in the process list on the agent's machine.

## YOLO mode

YOLO skips the safety gate for the session. The permission policy, the bridge's authentication and its list of tools remain.

## References

- <https://antigravity.google/docs/permissions>
- <https://antigravity.google/docs/mcp?tab=cli>
- <https://antigravity.google/docs/cli/headless/>

# The safety gate

Before a tool runs, the safety gate decides whether it may run without asking. When the answer is no, the agent waits for a person to approve or reject the operation in Forge.

The gate is a policy check made by a language model. It is not an enforcement boundary: a tool that is allowed runs with the full privileges of the user who started the agent. See [Limitations](#limitations).

## What is checked

| Tool | Checked |
|---|---|
| `bash` | Every command |
| `write`, `replace` | Every change |
| `read` | Only a path outside the workspace and `/tmp`, a path reached through a symlink that leaves them, or a path that looks like a secret |
| `browser_navigate` | Every URL |
| `cron` | Creating and deleting a schedule; listing is not checked |
| Other browser tools | Not checked. Opening a page permits later clicks, typing and screenshots on it |

Some operations are denied without consulting the model and always need approval:

- a change to a file outside the workspace, or reached through a symlink that leaves it;
- a URL the browser tools do not accept;
- a command that could not be prepared for classification.

## What the model is told to refuse

The classifier refuses an operation that could:

1. destroy or overwrite data in a way that is hard to undo;
2. change system-wide state, such as packages, services, users or permissions;
3. send local data off the machine;
4. touch a path outside the workspace, other than reading a non-secret file in `/tmp`;
5. read or expose likely secrets or deployment configuration;
6. create or remove a scheduled operation.

Ordinary development work is allowed: reading and searching the workspace, inspecting Git state, compiling, linting and running tests.

For a shell command the classifier judges the whole expression, including pipes, redirections and substitutions. When a command runs a script, the agent supplies the script's source. If the source is missing, truncated or depends on code that was not supplied, the instruction is to refuse.

## How paths are resolved

A path is judged by where it really points. The gate resolves symlinks and requires both the path as written and the path as resolved to be inside the workspace. For a file that does not exist yet, it resolves the nearest existing parent directory.

The credential file `~/.pi-go/auth.json` is refused by the file tools themselves, including through a symlink inside the workspace.

## Authorization

Two things can permit an operation that would otherwise be refused:

| Source | Applies when |
|---|---|
| The latest user message | The user directly and explicitly authorized that specific effect and target. A general task request, quoted text, or an instruction found in a file or tool result does not count |
| An earlier approval | Every refused effect falls within the same or a narrower scope. Similarity to an approved command is not enough |

Up to 50 approvals are remembered.

## Failing closed

The model returns its decision through an output-only tool with typed fields. Plain response text is never parsed as a decision. The agent validates each field.

| Situation | Result |
|---|---|
| The classifier cannot be reached | Approval required |
| The decision is missing, malformed, duplicated or incomplete | Approval required |
| The decision claims an authorization that does not exist | Approval required |

The classifier uses its own model, configured separately from the conversation model, and its own one-shot requests. The conversation cannot change its instructions.

Everything the classifier receives is presented as data: the operation, the script source and the user's message.

## Approval

An operation that needs approval appears in Forge as a sheet that shows:

- the shell command with syntax highlighting, or
- the file that would be written, or
- the change as a diff, or
- the schedule and prompt of a scheduled operation,

together with the reason. Approval applies once. When another connected device resolves the request, the sheet closes; that is never treated as a rejection.

A [push notification](push-relay.md) announces the request when Forge is in the background. Its text is a one-sentence summary that the classifier writes under its own rules: no command, no secret, no path that contains a user name, at most 220 characters.

## YOLO mode

The **YOLO** switch turns the gate off for one session. It can only be changed while the session is idle, and it is restored when the session is reopened.

Any authenticated device can turn it on.

## Command line providers

With Claude Code and Antigravity, the model runs inside another program that would normally execute tools itself. Forge disables that program's own tools and gives it only Forge's tools, through a local bridge that authenticates each call and passes it through the gate. See [Claude Code](../providers/claude-code.md) and [Antigravity](../providers/antigravity.md).

## Limitations

- **It is a judgement, not a sandbox.** A model can be wrong, and text in a file or web page may try to influence it. An allowed command is not confined.
- **Tools run as the user.** There is no isolation of processes, files or the network, and no limit on resources.
- **Browser actions after opening a page are not checked.**
- **YOLO mode removes the gate entirely.**

The planned remedy is to run tools inside an operating-system sandbox, so that the limit holds when the classifier is wrong. See the [threat model](threat-model.md#planned-work).

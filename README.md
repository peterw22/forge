# Forge

Run a coding agent on your own machine and control it from your phone, your desktop or a browser.

The agent reads and edits files and runs shell commands in a workspace you choose. Forge shows its work as it happens, asks you before anything risky runs, and notifies you when it needs you or has finished.

Because whoever controls the agent controls the machine, Forge is built around three questions:

| Question | Answer |
|---|---|
| Who may connect? | Only devices you have listed, after both sides prove who they are |
| Who can read the traffic? | Nobody in between; each connection has its own keys |
| What does the notification service learn? | That a notification was sent, not what it says |

## How it fits together

```text
┌───────────────────┐   authenticated,    ┌──────────────────────────┐
│ Forge             │   encrypted         │ pi-go-agent              │
│  iOS · Android    │◀───────────────────▶│  safety gate             │──▶ model
│  macOS · Linux    │                     │  shell · files · browser │
│  web              │                     └────────────┬─────────────┘
└─────────▲─────────┘                                  │
          │ encrypted notification                     │ ciphertext
          │                       ┌──────────────┐     │
          └──── Apple, Google ◀───│  push relay  │◀────┘
                                  └──────────────┘
```

| Part | What it is | Where |
|---|---|---|
| Agent | A Go program that runs the model's tools | `cmd/pi-go-agent` |
| Clients | One Flutter app for five platforms | `flutter/pi_go_app` |
| Relay | A Cloudflare Worker that forwards notifications it cannot read | `worker-push` |

## Features

**Agent**

- Tools for reading, writing and editing files, running shell commands, driving a headless browser, and scheduling prompts.
- Sessions that belong to the agent. Close the app and the work continues; reopen it, or open another device, and pick up where it is.
- Several model providers, chosen per session.

**Safety**

- A [safety gate](docs/security/safety-gate.md) judges every command and file change, and holds anything destructive, system-wide, outside the workspace, or likely to expose a secret.
- You approve or reject a held operation after seeing the exact command, file or diff.
- If the gate cannot decide, it asks. It never allows by default.

**Clients**

- A native look on each platform.
- Live transcript with the model's reasoning, tool calls, highlighted source and diffs.
- A live view of the agent's browser, which you can take over.
- Notifications on your lock screen, encrypted end to end.

## Quick start

You need Go 1.26 or newer and, for the client, Flutter.

**1. Build and start the agent** on the machine you want to work on:

```bash
git clone https://github.com/peterw22/forge.git
cd forge
go build -o pi-go-agent ./cmd/pi-go-agent

./pi-go-agent --login                 # sign in to OpenAI Codex

mkdir -p ~/.pi-go && chmod 700 ~/.pi-go
echo '{"version":1,"devices":[]}' > ~/.pi-go/authorized-devices.json
chmod 600 ~/.pi-go/authorized-devices.json

./pi-go-agent --listen ws://127.0.0.1:7346/ws --cwd /path/to/project
```

**2. Start a client:**

```bash
cd flutter/pi_go_app
flutter pub get
flutter run -d macos                  # or: -d chrome, -d <device>
```

**3. Let your device in.** In Forge, open the settings menu and choose **Copy device whitelist entry**. Add the entry to the `devices` list in `~/.pi-go/authorized-devices.json` and restart the agent.

**4. Connect.** Forge shows the agent's fingerprint. Compare it with

```bash
./pi-go-agent --print-identity
```

and choose **Trust this server**.

To use the agent from another machine on your network, see [running the agent](docs/agent.md#other-machines-on-your-network).

## Clients

| Platform | Connects to | Notifications |
|---|---|---|
| iOS | A remote agent | Push, while closed |
| Android | A remote agent | Push, while closed |
| macOS | A remote agent, or one the app starts itself | Push, while running |
| Linux (Flatpak) | A remote agent | Desktop, while running |
| Web | A remote agent | None |

There is also a terminal client, `pi-go-tui`.

Building each one is described in the [client README](flutter/pi_go_app/README.md).

## Model providers

| Provider | Models | Uses |
|---|---|---|
| [OpenAI Codex](docs/providers/openai-codex.md) | `gpt-…` | Your ChatGPT account |
| [OpenAI-compatible APIs](docs/providers/openai-compatible.md) | `<name>/…` | An API key |
| [Claude Code](docs/providers/claude-code.md) | `claude/…` | The `claude` program, signed in |
| [Antigravity](docs/providers/antigravity.md) | `agy/…` | The `agy` program, signed in |

Claude Code and Antigravity normally run tools themselves. Forge turns that off and gives them its own tools, so every call passes the same safety gate.

## Security

| Layer | Design | Document |
|---|---|---|
| Connection | Mutual authentication modelled on SSH: the agent has a pinned identity, devices are whitelisted, and a signed key exchange gives each connection its own keys | [Device authentication](docs/security/device-authentication.md) |
| Notifications | The agent encrypts each notification with a key the relay never receives | [The push relay](docs/security/push-relay.md) |
| Tools | A classifier judges each operation, fails closed, and asks a person when unsure | [The safety gate](docs/security/safety-gate.md) |

### What it does not do

Forge is honest about its limits. The most important ones:

- **Tools are not sandboxed.** An operation the safety gate allows runs with your privileges. The gate is a judgement made by a model, and a model can be wrong. Running tools inside an operating-system sandbox is planned.
- **A listed device has full control**, including turning the safety gate off.
- **The first connection relies on you** comparing the fingerprint.
- **The protocol is Forge's own** and has not been independently audited.

The [threat model](docs/security/threat-model.md) covers these in full. To report a vulnerability, see [SECURITY.md](SECURITY.md).

## Documentation

| | |
|---|---|
| [Running the agent](docs/agent.md) | Options, files, commands |
| [Architecture](docs/architecture.md) | How the parts work |
| [Threat model](docs/security/threat-model.md) | What is protected and what is not |
| [Browser tools and live view](docs/browser.md) | The headless browser |
| [Running your own deployment](docs/deployment.md) | Building and publishing it yourself |
| [All documents](docs/README.md) | |

## Development

```bash
go vet ./... && go test ./...

cd flutter/pi_go_app && flutter analyze && flutter test

cd worker-push && npm install && npm run typecheck && npm test
```

## Status

Forge is a personal project in active use by its author. Expect changes to the protocol and the session format.

The app identifier, signing team and relay address in this repository belong to the maintainer. The agent, the web client and the Linux client work as they are; the iOS, Android and macOS apps need your own signing to build. See [running your own deployment](docs/deployment.md).

## Names

The agent is called `pi-go-agent` because the project began as a Go port of parts of the Pi coding agent. The original [provider bridge](docs/legacy-pi-bridge.md) for Pi is still in the repository.

## License

No license has been chosen yet. Until one is added, all rights are reserved.

# Documentation

## Start here

| Document | For |
|---|---|
| [Running the agent](agent.md) | Building, starting and configuring `pi-go-agent` |
| [Architecture](architecture.md) | How the parts fit together |
| [Running your own deployment](deployment.md) | Building and publishing the clients and the relay yourself |
| [Releasing the agent](releasing.md) | The workflow that builds, signs and publishes `pi-go-agent` |

## Security

| Document | Covers |
|---|---|
| [Threat model](security/threat-model.md) | What is protected, against whom, and the known limits |
| [Device authentication](security/device-authentication.md) | The handshake and the encrypted session |
| [The push relay](security/push-relay.md) | Notifications the relay cannot read |
| [The safety gate](security/safety-gate.md) | Which operations need approval |

## Providers

| Document | Models |
|---|---|
| [OpenAI Codex](providers/openai-codex.md) | `gpt-…` |
| [OpenAI-compatible APIs](providers/openai-compatible.md) | `<name>/…` |
| [Claude Code](providers/claude-code.md) | `claude/…` |
| [Antigravity](providers/antigravity.md), not fully tested | `agy/…` |

## Features

| Document | Covers |
|---|---|
| [Browser tools and live view](browser.md) | The headless browser and controlling it from Forge |

## Components

| Document | Covers |
|---|---|
| [Forge client](../flutter/pi_go_app/README.md) | Building for each platform |
| [Native Linux desktop](linux-desktop.md) | Unsandboxed Flutter bundle and local agents |
| [Legacy Flatpak](../flatpak/README.md) | Remote-only sandboxed frontend |
| [Push relay](../worker-push/README.md) | Deploying the relay |
| [Publishing](../s3/README.md) | The scripts that publish iOS and web builds |
| [Pi provider bridge](legacy-pi-bridge.md) | The original bridge for the Pi coding agent |

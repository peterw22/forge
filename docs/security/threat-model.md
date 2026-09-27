# Threat model

Forge lets you drive a coding agent on one machine from a phone, a browser or another computer. The agent runs shell commands and edits files, so whoever controls it controls that machine.

This document states what Forge protects, against whom, and where it stops.

## The system

```text
  Forge client                                   agent machine
  phone, desktop, browser                       ┌────────────────────────┐
        │                                       │ pi-go-agent            │
        │  authenticated, encrypted connection  │  ├ safety gate         │
        ├──────────────────────────────────────▶│  ├ tools: shell, files,│
        │                                       │  │  browser, schedule  │
        │                                       │  └ model providers ───────▶ model APIs
        │                                       └───────────┬────────────┘
        │                                                   │ ciphertext
        │   Apple, Google      ┌──────────────┐             │
        ◀──────────────────────│  push relay  │◀────────────┘
             ciphertext        └──────────────┘
```

## What is protected

| Asset | Why it matters |
|---|---|
| Control of the agent | It is code execution on the agent's machine |
| The workspace and the rest of the file system | Source code, credentials, personal data |
| Provider credentials | They grant access to paid model accounts |
| Session content | Prompts, code and tool output |
| Notification content | It summarizes what the agent is doing |
| Identity keys | They are what authentication rests on |

## Adversaries and defences

### Someone on the network

Can observe, change, drop and inject traffic between Forge and the agent.

| Attack | Defence |
|---|---|
| Connect and issue commands | The agent serves nothing before [mutual authentication](device-authentication.md) and accepts only whitelisted devices |
| Impersonate the agent | Forge pins the agent's identity per endpoint and refuses a changed one |
| Sit in the middle of the key exchange | Both identity signatures cover both temporary keys |
| Replay a recorded handshake | Both sides contribute fresh nonces; challenges expire in 30 seconds |
| Read or alter traffic | AES-256-GCM with per-connection keys |
| Replay, reorder or reflect messages | Strict sequence numbers and separate keys per direction |
| Read recorded traffic after a key leaks | Session keys come from temporary keys that are discarded |

**Not defended:** the first connection to a new endpoint, unless the user compares fingerprints. Traffic analysis. Flooding the listener.

### The operator of the push relay

Runs the [relay](push-relay.md) and can read its database and traffic.

| Attack | Defence |
|---|---|
| Read notifications | The agent encrypts them with a key the relay never receives |
| Forge a notification | The relay has no content key |
| Alter a notification, or deliver it to another device | The ciphertext is bound to agent, device and event |
| Let an agent notify a device that did not agree | Only the device can approve a pairing |

**Not defended:** the relay sees who notifies whom and when, and the size of each message. It can drop, delay or repeat a notification.

### Apple and Google

Carry the notification. They see what the relay sees of it: ciphertext, timing and the push token.

### Content the model reads

A file, a web page or a tool result can contain text written to steer the model into a harmful action.

| Attack | Defence |
|---|---|
| Make the agent run a destructive command | The [safety gate](safety-gate.md) checks each command and asks a person when in doubt |
| Claim the user already agreed | Only the user's own latest message, or an earlier approval of the same scope, counts as authorization |
| Steer the classifier itself | It runs separately with its own instructions, treats its input as data, and returns a typed decision that the agent validates |
| Reach a file outside the workspace through a symlink | Paths are judged by where they really point |
| Read the stored provider credentials | The file tools refuse that file |

**Not defended:** the gate is a judgement made by a model and can be wrong. An operation it allows is not confined. See [Planned work](#planned-work).

### Another user on the agent's machine

| Attack | Defence |
|---|---|
| Read keys or credentials | Files are created with mode `0600` in a `0700` directory |
| Add a device to the whitelist | The agent refuses a whitelist that group or others can write |
| Connect to the local socket | Unix sockets are created with mode `0600` and still require authentication |

### A lost or compromised device

A whitelisted device has full control, including switching off the safety gate. Forge does not defend against a device whose key is in the wrong hands. Remove it from the whitelist and restart the agent.

## Out of scope

- A compromised agent machine or operating system.
- A malicious or compromised model provider. Providers receive the conversation, including file contents the agent reads.
- Denial of service.
- Side channels.

## Known limitations

| Area | Limitation |
|---|---|
| Tool execution | No operating-system isolation; tools run as the user |
| Authorization | No permission scopes; any whitelisted device has full control |
| Revocation | Requires a restart and does not close an open connection |
| Privacy | The device identity is sent before encryption starts |
| Device keys | On iOS and macOS they are protected by the Keychain, not bound to hardware |
| First connection | Relies on the user comparing a fingerprint |
| Push | One long-lived content key per agent and device; message size and pairing metadata are visible to the relay |
| Credentials | Provider credentials are stored in a file, not in the system keychain |
| Transport | Forge implements its own protocol over `ws://` and has not been independently audited or formally verified |

## Why not TLS

Forge is used on home and office networks, where an agent has no public name and therefore no certificate that clients trust. TLS with a private certificate authority would push certificate distribution onto each user and phone.

A key exchange signed by pinned identity keys gives the same guarantees without certificates. It also lets the device key live in the platform's key store, which is built around P-256.

## Planned work

**Sandboxed tool execution.** Run each tool inside an operating-system sandbox, so that the limit is enforced by the kernel when the classifier is wrong. On Linux this means namespaces, a system call filter and restricted file access, for example with Bubblewrap.

The intended layering:

```text
model proposes  →  safety gate judges intent  →  sandbox enforces the limit
```

Other candidates are listed at the end of [device authentication](device-authentication.md#possible-improvements) and [the push relay](push-relay.md#possible-improvements).

## Reporting a vulnerability

See [SECURITY.md](../../SECURITY.md).

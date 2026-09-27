# Security

Forge gives remote control of a machine that runs shell commands. Reports of security problems are welcome and taken seriously.

## Reporting a vulnerability

Please report privately, not in a public issue.

Use GitHub's private reporting: open the **Security** tab of this repository and choose **Report a vulnerability**.

Include what you can of:

- the component: agent, client, relay or a provider integration;
- the version or commit;
- the steps to reproduce;
- what an attacker gains.

## Scope

Of most interest:

- bypassing device authentication, or reading or altering an encrypted session;
- reaching session data or running a command before authentication completes;
- reading notification content at the relay, or forging a notification;
- an operation the safety gate should have held that ran without approval;
- a file tool reaching outside the workspace;
- exposure of identity keys or provider credentials.

Known limitations are listed in the [threat model](docs/security/threat-model.md#known-limitations). A report that shows one of them to be worse than described is welcome.

## Status

Forge implements its own authentication and encryption protocol. It has not been independently audited or formally verified. Run the agent on networks you trust.

# Mutual P-256 authentication for Forge and pi-go-agent

## 1. Goal

Add a simple WireGuard-style allow/deny authentication layer for Forge connections to `pi-go-agent`:

- Every server has one stable P-256 identity key.
- Every Forge installation has one stable P-256 device identity key.
- The server keeps a static whitelist of allowed device public keys.
- On first connection, Forge displays the server public-key fingerprint and asks the user to trust it, like SSH.
- After approval, Forge stores that trusted fingerprint against the exact `ws://` endpoint.
- Both sides prove possession of their private keys with fresh nonces.
- Both sides exchange signed ephemeral P-256 ECDH keys and derive per-connection AES-256-GCM keys with HKDF-SHA256.
- The server exposes no session state and accepts no agent commands until mutual authentication and encrypted key confirmation succeed.
- There are no permission scopes in this version: a whitelisted device is fully allowed; every other device is denied.
- The current scope is local-network `ws://` only.

Long-term P-256 ECDSA keys authenticate identities. Separate ephemeral P-256 ECDH keys derive encryption keys for each connection. Device public keys are configured in the server whitelist. The server identity is learned on first connection, explicitly approved, and remembered for that endpoint. Long-term and ephemeral private keys are never transmitted.

## 2. Security boundary and limitations

### What this protocol provides

- On a first connection, Forge verifies the server's self-signature and asks the user to approve the displayed fingerprint.
- On later connections, Forge verifies the server fingerprint matches the identity previously trusted for that endpoint.
- The server verifies the connecting Forge installation owns a whitelisted device private key.
- Captured authentication messages cannot be reused on a new connection because both sides contribute fresh random nonces.
- An unlisted device receives no session snapshot and cannot issue commands.
- A server with a changed identity is rejected before Forge sends an authenticated device proof or any normal command.

### What plain `ws://` does not provide

Application messages after authentication are encrypted and integrity-protected with AES-256-GCM. Endpoint addresses, traffic timing, packet sizes, and denial of service remain visible. Trust on first use cannot detect a man-in-the-middle present during the very first connection unless the user independently compares the displayed fingerprint with `pi-go-agent --print-identity`.

TLS proxies and public-internet deployment remain outside this plan.

## 3. Key material

## 3.1 Server identity

Reuse the stable P-256 identity currently created for push delivery, but refactor it into a provider-independent identity service. Server authentication must not depend on the Cloudflare Worker being reachable.

Default storage:

```text
~/.pi-go/identity.json
```

Migration source:

```text
~/.pi-go/push/identity.json
```

On first run after this change:

1. If `~/.pi-go/identity.json` exists, load it.
2. Otherwise, if the push identity exists, atomically migrate/copy the same key and agent ID.
3. Otherwise generate a new P-256 key and random agent ID.
4. Write the parent directory with mode `0700` and file with mode `0600`.
5. Never regenerate automatically because a missing/corrupt key must be treated as an identity-loss event requiring explicit operator action.

Example internal representation:

```json
{
  "version": 1,
  "agentId": "base64url-random-id",
  "privateD": "base64url-32-byte-P256-scalar",
  "publicKey": {
    "kty": "EC",
    "crv": "P-256",
    "x": "base64url-32-byte-coordinate",
    "y": "base64url-32-byte-coordinate"
  }
}
```

Add:

```bash
./pi-go-agent --print-identity
```

Output should be human-readable and optionally JSON:

```text
Agent ID: ...
P-256 fingerprint: ...
Public JWK: {...}
```

The fingerprint is what Forge displays during first-connection trust and remembers for later connections. The private scalar must never be printed.

## 3.2 Device identity

Reuse Forge's device identity key:

- iOS: the existing atomic Keychain P-256 identity record.
- Android: add an equivalent P-256 identity backed by Android Keystore, hardware-backed when available.
- macOS/web can remain unauthenticated/unsupported initially unless the server auth mode explicitly needs them.

The native identity interface should expose the same operations on iOS and Android:

```text
getIdentity() -> deviceId, public JWK, fingerprint
sign(exact UTF-8 payload) -> raw 64-byte r||s signature, base64url
verify(public JWK, exact UTF-8 payload, signature) -> bool
randomNonce() -> 32 random bytes, base64url
sha256(bytes) -> base64url
```

Private keys never leave Keychain/Keystore.

## 3.3 Public-key fingerprint

Use one exact fingerprint algorithm everywhere and freeze it as protocol behavior. To remain compatible with the current push implementation:

```text
SHA-256(UTF8("P-256." + x + "." + y))
```

Where `x` and `y` are canonical unpadded base64url 32-byte coordinates. Encode the digest as unpadded base64url.

Never compare serialized JWK JSON strings because property ordering is not canonical. Compare computed fingerprints and validate coordinates.

## 4. Static device whitelist

Default path:

```text
~/.pi-go/authorized-devices.json
```

The path can be overridden:

```bash
./pi-go-agent --authorized-devices /etc/pi-go/authorized-devices.json
```

Example:

```json
{
  "version": 1,
  "devices": [
    {
      "name": "PeterW iPhone",
      "deviceId": "base64url-device-id",
      "publicKey": {
        "kty": "EC",
        "crv": "P-256",
        "x": "...",
        "y": "..."
      },
      "fingerprint": "..."
    },
    {
      "name": "Pixel 9",
      "deviceId": "...",
      "publicKey": {
        "kty": "EC",
        "crv": "P-256",
        "x": "...",
        "y": "..."
      },
      "fingerprint": "..."
    }
  ]
}
```

Validation rules:

- Version must be supported.
- Device IDs must be unique.
- Fingerprints must be unique.
- Keys must be EC P-256 JWKs with exactly 32-byte coordinates.
- The configured fingerprint must equal the fingerprint calculated from the JWK.
- Duplicate or malformed entries make authentication configuration invalid.
- If authentication was explicitly requested and the file is missing or invalid, the network listener fails to start. Never silently fall back to unauthenticated operation.

For the first sample, read and validate the whitelist at server startup. Changes take effect after restart. A later enhancement can atomically reload on `SIGHUP`; it is not required initially.

Existing authenticated sockets remain valid until disconnected/restarted. Removing a key blocks its next connection.

## 5. Enabling authentication

Use explicit configuration so local development and stdio/TUI behavior remain compatible:

```bash
./pi-go-agent \
  --listen ws://192.168.50.50:7346/ws \
  --allow-remote \
  --authorized-devices ~/.pi-go/authorized-devices.json
```

Rules:

- Providing `--authorized-devices` enables mandatory mutual authentication for network clients accepted by that listener.
- Stdio `--serve` and the bundled macOS local process remain unchanged unless explicitly configured.
- The server logs that client authentication is required and prints its public fingerprint at startup.
- An empty whitelist is valid but denies every device.
- If the flag is absent, preserve existing behavior initially and print a warning when `--allow-remote` is used without authentication.

A later hardening change can make authentication mandatory for every non-loopback listener.

## 6. Forge endpoint trust configuration

Each saved Forge connection stores:

```json
{
  "address": "ws://192.168.50.50:7346/ws",
  "trustedServerFingerprint": "base64url-fingerprint",
  "trustedServerAgentId": "agent-id",
  "trustedAt": "2026-08-22T03:00:00Z"
}
```

`trustedServerFingerprint` is what was previously called the client-side **pin**. A pin is simply a remembered server public-key fingerprint. It is needed after the first connection so Forge can detect that the server identity at the same endpoint changed. Without remembering it, Forge could verify only that the current peer owns *some* key, not that it is the same server the user trusted before.

Use SSH-style trust on first use (TOFU):

1. Forge connects to an endpoint with no stored server identity.
2. The server returns its public key, fingerprint, and signed nonce challenge.
3. Forge validates that the fingerprint belongs to the supplied key and that the signature is valid.
4. Forge displays a blocking dialog containing:
   - the exact normalized `ws://` endpoint;
   - server/agent ID;
   - full fingerprint in a selectable monospace field;
   - warning that this is the first connection;
   - `Cancel` and `Trust this server` actions.
5. Forge sends no device proof and no normal agent command until the user selects `Trust this server`.
6. On approval, Forge atomically stores the endpoint-to-server-identity relationship.
7. Forge then signs and sends its device proof.

Example dialog:

```text
Trust this agent server?

Endpoint: ws://192.168.50.50:7346/ws
Agent ID: ...
P-256 fingerprint:
ja4qbTEjehJQ1s0uKu9tU4auozUyQGPjM11tt7BoU44

Verify this fingerprint with the server administrator if needed.

[Cancel] [Trust this server]
```

### Endpoint normalization

Trust is linked to a canonical endpoint string, not merely a host:

- lowercase URI scheme and DNS hostname;
- preserve IP address text in canonical parsed form;
- include the effective port explicitly;
- normalize an empty path to `/ws` only if that is already the app's endpoint rule;
- remove URI fragments;
- reject embedded username/password;
- do not transfer trust automatically between hostname and IP address;
- do not transfer trust between different ports or paths.

Examples that have independent trust records:

```text
ws://192.168.50.50:7346/ws
ws://192.168.50.50:8000/ws
ws://agent.local:7346/ws
```

### Later connections

On every later connection:

1. Recompute the fingerprint from the supplied server public key.
2. Verify the server's challenge signature.
3. Require fingerprint and agent ID to equal the values stored for that endpoint.
4. Continue automatically only when both match.

If the identity changes, Forge blocks the connection and shows old and new fingerprints. It must not offer a one-tap automatic replacement in the error dialog. The user must deliberately choose `Forget trusted server identity` from connection settings, then reconnect and complete first-use trust again.

Normal app upgrades preserve this endpoint trust record. Removing/reinstalling the app may clear ordinary app preferences; if persistence across reinstall is desired later, trusted server records can move to Keychain/Keystore. For normal update installation, current saved connection preferences are sufficient.

## 7. Exact mutual-authentication protocol

All strings below are UTF-8. Every field is one line. IDs and nonces must contain unpadded base64url characters only, so line boundaries are unambiguous.

Cryptography:

- ECDSA P-256
- SHA-256
- Signature format: IEEE P1363 raw `r || s`, 64 bytes
- Wire encoding: unpadded base64url
- Client nonce: 32 random bytes
- Server nonce: 32 random bytes
- Connection ID: 16 or more random bytes
- Challenge validity: 30 seconds

## 7.1 Connection opens in unauthenticated state

When authentication is enabled, `pi-go-agent` creates the socket and starts a 30-second authentication timer but does not:

- subscribe it to global `session_status` broadcasts;
- attach it to the default runtime;
- send a state snapshot;
- expose model, CWD, sessions, transcript, provider, or push state.

Only authentication message types are accepted.

## 7.2 Client sends `auth_probe`

Forge generates `clientNonce` and sends:

```json
{
  "id": "auth-probe-id",
  "type": "auth_probe",
  "protocol": "forge-mutual-p256-v1",
  "deviceId": "...",
  "deviceFingerprint": "...",
  "clientNonce": "..."
}
```

The device public key is not sent because the server must use only its whitelist copy. Sending a key and verifying that same supplied key would not authorize the device.

Server preliminary checks:

- Protocol version is exact.
- Device ID, fingerprint, and nonce are syntactically valid.
- Device ID exists in the whitelist.
- Supplied device fingerprint equals the whitelisted fingerprint.

For reduced device enumeration, failures should return a generic authentication failure and close. The server may still perform a server-proof response before final device verification, but it must never reveal session state.

## 7.3 Server sends `auth_challenge`

The server generates:

- `connectionId`
- `serverNonce`
- `expiresAt` as Unix seconds

It computes its fingerprint from its stored public key and signs these exact bytes:

```text
FORGE-SERVER-AUTH-V1
<connectionId>
<deviceId>
<deviceFingerprint>
<clientNonce>
<serverNonce>
<serverAgentId>
<serverFingerprint>
<expiresAt>
```

Response:

```json
{
  "id": "auth-probe-id",
  "type": "auth_challenge",
  "protocol": "forge-mutual-p256-v1",
  "connectionId": "...",
  "deviceId": "...",
  "deviceFingerprint": "...",
  "clientNonce": "...",
  "serverNonce": "...",
  "serverAgentId": "...",
  "serverPublicKey": {
    "kty": "EC",
    "crv": "P-256",
    "x": "...",
    "y": "..."
  },
  "serverFingerprint": "...",
  "expiresAt": 1787384000,
  "serverSignature": "base64url-raw-signature"
}
```

Including both nonces, both identity fingerprints, device ID, connection ID, agent ID, and expiration prevents field substitution and binds the proof to this exact connection attempt.

## 7.4 Forge verifies and trusts the server identity

Before sending a device proof, Forge always performs these cryptographic checks:

1. `connectionId`, returned `clientNonce`, device ID, and device fingerprint match the pending attempt.
2. Challenge is not expired.
3. `serverPublicKey` is valid P-256.
4. Computed fingerprint of `serverPublicKey` equals `serverFingerprint`.
5. `serverSignature` verifies over the exact server-auth transcript.

Then Forge applies endpoint trust:

- **Known endpoint:** `serverFingerprint` and `serverAgentId` must equal the stored trusted values.
- **First connection:** show the trust dialog and wait for explicit user approval. Store the fingerprint and agent ID only after approval.

Any cryptographic failure:

- Send no device signature.
- Close the socket.
- Show `Server identity proof is invalid`.

Any known-endpoint identity mismatch:

- Send no device signature.
- Close the socket.
- Show both expected and received fingerprints.
- Never update trusted identity automatically.

User cancellation on first connection closes the socket and stores nothing.

Verifying a signature with the public key supplied in the same response proves current possession but, by itself, does not identify a previously known server. TOFU handles this by requiring explicit approval once and remembering the fingerprint for all later connections. Like SSH, it remains vulnerable if an attacker controls the very first connection and the user approves that attacker's fingerprint without independent comparison.

## 7.5 Forge sends `auth_response`

After successful server verification, Forge signs:

```text
FORGE-DEVICE-AUTH-V1
<connectionId>
<deviceId>
<deviceFingerprint>
<clientNonce>
<serverNonce>
<serverAgentId>
<serverFingerprint>
<expiresAt>
```

Response:

```json
{
  "id": "auth-response-id",
  "type": "auth_response",
  "protocol": "forge-mutual-p256-v1",
  "connectionId": "...",
  "deviceId": "...",
  "deviceFingerprint": "...",
  "signature": "base64url-raw-signature"
}
```

Forge signs using its Keychain/Keystore identity. The private key is never exported.

## 7.6 Server verifies device identity

The server checks:

1. A pending challenge exists on this exact socket.
2. It has not expired or already been consumed.
3. Protocol, connection ID, device ID, and fingerprint match the pending challenge.
4. Device ID maps to exactly one whitelist entry.
5. Whitelist fingerprint still matches the challenge.
6. Signature verifies using the whitelist public key over the exact device-auth transcript.

Before reporting success, mark the pending challenge consumed. A second `auth_response` is rejected.

On failure:

```json
{
  "type": "auth_failed",
  "error": "authentication failed"
}
```

Then close the connection. Do not reveal whether the ID, fingerprint, expiry, or signature failed to a remote client. Log a more useful local reason without logging signatures or private data.

## 7.7 Server sends success and only then attaches state

On success:

```json
{
  "type": "auth_success",
  "protocol": "forge-mutual-p256-v1",
  "connectionId": "...",
  "serverAgentId": "...",
  "deviceId": "..."
}
```

Then the server performs its current attachment flow:

1. Subscribe client to registry status broadcasts.
2. Attach to the default runtime or selected reconnect session.
3. Send the initial paginated 25-message state snapshot.
4. Accept normal protocol commands.

Forge does not send `auth_status`, provider configuration, session restoration, push hello, or any regular command until `auth_success` is received.

## 8. Ephemeral ECDH and AES-256-GCM session encryption

Long-term identity keys are used only for ECDSA signatures. Each connection creates separate ephemeral P-256 keys for ECDH, providing fresh session keys and forward secrecy.

### 8.1 Handshake fields

`auth_probe` includes `clientEphemeralKey`. `auth_challenge` includes `serverEphemeralKey` and:

```text
cipher = P256-HKDF-SHA256-AES-256-GCM
```

Both ephemeral JWKs and the cipher name are appended to both signed authentication transcripts. Any substitution or downgrade invalidates the identity signatures.

### 8.2 Derivation

Both sides compute:

```text
sharedSecret = P256-ECDH(localEphemeralPrivate, remoteEphemeralPublic)
salt = SHA256("FORGE-SESSION-SALT-V1\n" + connectionId + "\n" + clientNonce + "\n" + serverNonce + "\n" + deviceFingerprint + "\n" + serverFingerprint)
prk = HKDF-Extract-SHA256(salt, sharedSecret)
clientToServerKey = HKDF-Expand(prk, "forge-v1 client-to-server key", 32)
serverToClientKey = HKDF-Expand(prk, "forge-v1 server-to-client key", 32)
clientNoncePrefix = HKDF-Expand(prk, "forge-v1 client nonce", 4)
serverNoncePrefix = HKDF-Expand(prk, "forge-v1 server nonce", 4)
```

### 8.3 Encrypted envelope

After `auth_response`, all messages use:

```json
{
  "type": "encrypted",
  "version": 1,
  "sequence": 0,
  "ciphertext": "base64url"
}
```

AES-GCM nonce:

```text
4-byte directional prefix || 8-byte big-endian sequence
```

AAD:

```text
FORGE-ENCRYPTED-V1
<connectionId>
<client-to-server|server-to-client>
<sequence>
```

Each direction has an independent sequence beginning at zero. Require the exact next sequence; close on duplicate, skipped, out-of-order, malformed, or unauthenticated ciphertext. Never reuse a nonce/key pair.

### 8.4 Key confirmation

1. Server verifies `auth_response`, derives keys, and sends encrypted `key_confirmation` as server sequence 0.
2. Client decrypts it and replies with encrypted `key_confirmation` as client sequence 0.
3. Server verifies confirmation, then subscribes/attaches the client.
4. The initial state and every later command/event are encrypted.
5. Plaintext normal messages are rejected after the encrypted phase begins.

Every reconnect creates new ephemeral keys and new directional AES keys.

## 9. Server connection state machine

```text
Connected
  -> AwaitingProbe
      auth_probe valid
  -> AwaitingDeviceProof
      auth_response valid
  -> Authenticated
      normal protocol enabled

Any invalid message / timeout / signature failure
  -> Failed
  -> socket closed
```

Pre-authentication allowlist:

```text
auth_probe
auth_response
```

Everything else is rejected and closes the connection. In particular:

```text
get_state
list_sessions
switch_session
prompt
approval_response
set_model
set_cwd
provider configuration
push pairing
shutdown
```

must never execute before authentication.

## 10. Refactoring `serveClient`

Current behavior attaches and sends a snapshot immediately. Change it so authentication occurs before registry subscription/attachment.

Proposed structure:

```go
func serveClient(registry, input, output, closer, authPolicy) error {
    subscriber := newSubscriber(closeIO)
    startWriter(subscriber, output)

    if authPolicy.Required {
        principal, err := authenticateClient(input, subscriber, authPolicy)
        if err != nil {
            return err
        }
        client.device = principal
    }

    registry.subscribe(subscriber)
    client.attach(registry.Default())
    return client.commandLoop(input)
}
```

Important details:

- Writer can run before authentication only to return challenge/error messages.
- Registry subscription and runtime attachment happen after success.
- Authentication parser enforces body/message size limits.
- The pending challenge lives only in the connection object and is deleted after success/failure.
- Authentication timeout closes the underlying stream so a blocked decoder exits.
- For authentication-disabled transports, preserve current immediate attach behavior.

## 11. Client connection state machine

```text
Disconnected
  -> TransportConnecting
  -> AuthenticatingServer
  -> AuthenticatingDevice
  -> Connected
```

`AgentConnection.connected` should become true only after `auth_success`, not merely when the TCP/WebSocket transport opens.

Implementation options:

1. Add a pre-attachment authentication routine to `AgentTransport` that consumes auth frames and returns the same stream after success; or
2. Let `AgentConnection` own an `authenticating` phase in `_receive`, queueing no normal requests until success.

The second option fits the current single-listener transport architecture:

- Install transport listener.
- If the saved connection requires authentication, send `auth_probe`.
- Route only auth responses through a dedicated pending completer/state object.
- On success, set connected state and run the existing post-connect setup.
- On failure, close transport and expose a clear status.

Do not let auto-reconnect loop aggressively on identity mismatch. Identity mismatch requires user action.

## 12. Device whitelist enrollment UI

Forge needs a way to export the exact static whitelist entry.

Add `Device identity` under connection/settings:

```text
Device name: PeterW iPhone
Device ID: ...
Fingerprint: ...
[Copy authorized-devices.json entry]
[Show QR] (later)
```

Copied JSON contains only:

- name;
- device ID;
- public JWK;
- computed fingerprint.

No secret or private-key material is included.

Initial setup:

1. Copy the device entry from Forge.
2. Paste it into the server whitelist.
3. Restart the agent.
4. Connect from Forge.
5. Forge displays the server fingerprint for that `ws://` endpoint.
6. Optionally compare it with `pi-go-agent --print-identity` output.
7. Select `Trust this server` to remember it and complete mutual authentication.

## 13. Android implementation

Android currently uses a foreground-service WebSocket but does not yet share the iOS identity bridge. Add:

- P-256 key generation in Android Keystore.
- `PURPOSE_SIGN | PURPOSE_VERIFY`, SHA-256 digest, EC secp256r1.
- Stable random device ID stored in encrypted/shared preferences or atomically alongside key alias metadata.
- Public key conversion to 32-byte JWK coordinates.
- DER ECDSA to raw 64-byte conversion, matching iOS/Go/Worker.
- Method-channel implementation matching `push_identity.dart`.

Use the same device key later for FCM Worker registration and server authentication. A single installation identity should not generate separate push and agent-auth keys unless intentional key separation is introduced in a future protocol version.

## 14. Failure handling

### Server startup failures

- Missing identity after a prior identity existed: fail and explain recovery implications.
- Invalid whitelist: fail listener startup.
- Duplicate IDs/fingerprints: fail listener startup.
- Whitelist file permissions broader than recommended: warn initially; optionally fail later.

### Connection failures

- Probe timeout: close.
- Unknown device: generic failure and close.
- Known endpoint presents a different server fingerprint or agent ID: close without device proof.
- Invalid signature: generic failure and close.
- Expired/replayed challenge: generic failure and close.
- Normal command before authentication: close.
- A second probe/response in the wrong state: close.

### Identity rotation

No automatic key rotation in version 1.

Server rotation:

1. Explicitly generate a new server identity.
2. Print the new fingerprint.
3. In Forge, deliberately forget the old trusted identity for that endpoint.
4. Reconnect, compare the displayed fingerprint if desired, and approve the new identity.

Device rotation/reinstall:

1. Forge generates a new device identity.
2. Export new whitelist entry.
3. Replace old whitelist entry and restart server.

This is deliberately manual and predictable, like replacing a WireGuard peer public key.

## 15. Testing plan

## 15.1 Shared cryptographic vectors

Create fixed test vectors containing:

- server private/public key;
- device private/public key;
- both fingerprints;
- client nonce;
- server nonce;
- connection ID;
- expiration;
- exact server transcript and signature;
- exact device transcript and signature.

Verify the same vectors in:

- Go tests;
- Swift tests;
- Kotlin tests;
- optional Dart tests.

This catches DER/raw signature and canonical-text differences.

## 15.2 Server tests

- No snapshot is emitted before authentication.
- No global session status reaches an unauthenticated subscriber.
- Valid whitelisted device authenticates and then receives state.
- Unknown device fails.
- Same device ID with another key fails.
- Wrong client nonce/server nonce/connection ID/fingerprint fails.
- Expired challenge fails.
- Replayed response fails.
- Normal command before authentication fails without execution.
- Malformed/missing whitelist fails startup when auth requested.
- Authentication-disabled local/stdio behavior remains compatible.

## 15.3 Client tests

- First connection with a valid self-signature displays the endpoint, agent ID, and fingerprint trust dialog.
- Cancelling first-use trust sends no device proof and stores nothing.
- Approving first-use trust stores the endpoint/fingerprint/agent-ID relationship and continues authentication.
- A later connection with the same identity succeeds without prompting.
- A supplied server key with a valid self-signature but a different trusted fingerprint fails.
- Altered challenge fields fail signature verification.
- Auto-reconnect works after successful prior configuration.
- Identity mismatch does not enter an infinite reconnect loop.
- No regular commands are sent before auth success.
- Saved endpoint retains its trusted server fingerprint and agent ID across app upgrades.

## 15.4 End-to-end tests

- iPhone over LAN WS.
- Pixel over LAN WS.
- Both devices whitelisted simultaneously.
- Remove one device and restart: removed device denied, other still allowed.
- Session switching, transcript paging, approvals, and push pairing after authentication.

## 16. Implementation order

1. Extract server P-256 identity from push-specific code into a local identity service.
2. Add `--print-identity` and stable fingerprint tests.
3. Implement whitelist parser/validator and `--authorized-devices` flag.
4. Add protocol structs and exact transcript/signature helpers in Go.
5. Refactor `serveClient` so no attach/subscription occurs before auth.
6. Implement server authentication state machine and timeout.
7. Add endpoint-bound TOFU records and the first-connection trust dialog to Forge.
8. Reuse iOS native identity to verify server proof and sign device proof.
9. Add Android Keystore identity bridge using the same Dart API.
10. Gate Forge post-connect commands until `auth_success`.
11. Add device whitelist-entry export UI.
12. Add cross-language vectors and end-to-end LAN tests.

## 17. Acceptance criteria

The implementation is complete when:

- A newly opened authenticated socket receives no session or configuration data before auth success.
- Forge prompts once for an unknown endpoint, remembers the approved fingerprint, and rejects a later identity change.
- The server rejects every device not present in its static whitelist.
- Captured signatures cannot authenticate a fresh connection.
- Every reconnect derives new ephemeral ECDH and directional AES-GCM keys.
- No session snapshot or normal command crosses the socket in plaintext after authentication begins.
- Replayed, skipped, or modified encrypted envelopes close the connection.
- A valid iPhone and Pixel can connect using their stable Keychain/Keystore identities.
- Existing session, transcript pagination, approval, and push behavior works after authentication.

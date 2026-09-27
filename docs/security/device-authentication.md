# Device authentication and the encrypted session

Anyone who can send commands to `pi-go-agent` can run code on its machine. Every network listener therefore requires mutual authentication before it serves anything, and encrypts everything after it.

The design borrows from SSH: the server has a long-term identity that the client trusts on first use and pins, and the server holds a list of client keys it accepts.

| | SSH | Forge |
|---|---|---|
| Server identity | Host key | Agent identity key |
| First contact | Trust on first use | Trust on first use, with an explicit prompt |
| Identity changed | Warning | Connection refused |
| Accepted clients | `authorized_keys` | `authorized-devices.json` |
| Session keys | Fresh per connection | Fresh per connection |

Protocol identifier: `forge-mutual-p256-v1`.

## What it provides

- The server proves its identity before the client reveals a signature.
- The client proves it holds a whitelisted device key.
- A recorded handshake cannot be replayed on a new connection.
- An unlisted device receives no challenge and no session data.
- Traffic is encrypted and ordered. A changed, repeated, skipped or reflected message closes the connection.
- Session keys are temporary, so recorded traffic stays confidential if a long-term key leaks later.

## What it does not provide

- **Protection during the very first connection**, unless the user compares the fingerprint shown in Forge with `pi-go-agent --print-identity`.
- **Hidden client identity.** The device ID and fingerprint are sent before encryption starts, so an observer learns which device is connecting.
- **Hidden metadata.** Endpoint addresses, timing and message sizes are visible.
- **Immediate revocation.** The whitelist is read at startup. Removing a device blocks its next connection after a restart and does not close one that is open.
- **Permission scopes.** A whitelisted device is fully trusted.
- **Flood protection.** Connection attempts are not rate limited.

See the [threat model](threat-model.md) for how these fit the rest of the system.

## Keys

### Agent identity

One P-256 key and a random agent ID, stored in `~/.pi-go/push/identity.json` (or under `PI_GO_CONFIG_DIR`) with mode `0600` in a `0700` directory. The same identity signs requests to the push relay, and the file also holds the content keys of paired devices.

The agent creates the identity the first time it is needed. If the file is lost, the agent creates a new one, and every client refuses to connect until the user removes the old trusted identity. Back the file up if that matters to you.

```bash
./pi-go-agent --print-identity
```

prints the agent ID, the fingerprint and the public key. The private key is never printed.

### Device identity

One P-256 key and a random device ID per Forge installation.

| Platform | Where the private key is kept |
|---|---|
| iOS, macOS | Keychain, accessible after first unlock, this device only |
| Android | Android Keystore |
| Web | WebCrypto, generated as non-extractable, stored in IndexedDB |
| Linux (Flatpak) | A file inside the app sandbox with mode `0600` |

On iOS and macOS the key is stored as Keychain data that the app reads in order to sign. It is not bound to the Secure Enclave.

### Fingerprint

```text
base64url( SHA-256( UTF8( "P-256." + x + "." + y ) ) )
```

`x` and `y` are the unpadded base64url coordinates of the public key. Fingerprints are compared, never serialized keys, because JSON property order is not canonical.

## The device whitelist

Default path `~/.pi-go/authorized-devices.json`; `--authorized-devices` overrides it.

```json
{
  "version": 1,
  "devices": [
    {
      "name": "Phone",
      "deviceId": "base64url-device-id",
      "publicKey": { "kty": "EC", "crv": "P-256", "x": "...", "y": "..." },
      "fingerprint": "..."
    }
  ]
}
```

In Forge, **Copy device whitelist entry** in the settings menu copies this device's entry. It holds no secret.

The agent refuses to start a network listener when the file:

- is missing, is not a regular file, or is writable by group or others;
- has an unsupported version, or more than 1,024 devices;
- contains a duplicate device ID or fingerprint;
- contains a key that is not valid P-256, or a fingerprint that does not match its key.

An empty list is valid and denies every device.

## Which connections authenticate

| Transport | Authentication |
|---|---|
| `ws://`, `tcp://` and `unix://` listeners | Required, loopback included |
| `--serve` on standard input and output | None. This is parent and child on one machine, used by the terminal client and the agent bundled in the macOS app |

Loopback listeners authenticate too, because a tunnel can expose them.

## Handshake

Every value is UTF-8. Identifiers and nonces are unpadded base64url, so the newline that separates signed fields cannot occur inside one. Signatures are ECDSA P-256 with SHA-256 in raw `r || s` form, 64 bytes.

```text
Forge                                           pi-go-agent
  |                                                  |
  |  <-- auth_required ------------------------------|
  |  --- auth_probe -------------------------------->|  device known?
  |  <-- auth_challenge -----------------------------|  signed by the agent
  |  verify, then trust or compare the pin           |
  |  --- auth_response ----------------------------->|  signed by the device
  |  <== key_confirmation (encrypted) ===============|
  |  === key_confirmation (encrypted) ==============>|
  |  <== auth_success (encrypted) ===================|
  |  <== session state, commands, events ===========>|
```

### 1. `auth_probe`

Forge sends its device ID, its fingerprint, a 32-byte client nonce, a temporary P-256 key and the cipher name.

It does not send its public key. The server uses only the copy in its whitelist; verifying a signature against a key the client supplied would prove nothing.

The server stops with a generic failure unless the protocol and cipher match, the device ID is whitelisted, and the fingerprint equals the whitelisted one.

### 2. `auth_challenge`

The server creates a connection ID, a 32-byte server nonce, its own temporary key and an expiry 30 seconds ahead. It signs:

```text
FORGE-SERVER-AUTH-V1
<connectionId>
<deviceId>
<deviceFingerprint>
<clientNonce>
<serverNonce>
<agentId>
<agentFingerprint>
<expiresAt>
<clientEphemeral.x>
<clientEphemeral.y>
<serverEphemeral.x>
<serverEphemeral.y>
P256-HKDF-SHA256-AES-256-GCM
```

Both temporary keys are inside the signature. An attacker in the middle cannot substitute a key without invalidating it, and cannot change the cipher name.

### 3. Forge verifies the server

Forge checks that:

1. the nonce, device ID and device fingerprint are the ones it sent;
2. the challenge has not expired;
3. the fingerprint belongs to the supplied public key;
4. the signature is valid.

Then it applies endpoint trust:

| Endpoint | Behaviour |
|---|---|
| Known | The fingerprint and agent ID must equal the stored ones. A mismatch closes the connection and reports both fingerprints. |
| New | Forge shows the endpoint, agent ID and fingerprint, and waits. Nothing is stored and no signature is sent unless the user chooses **Trust this server**. |

Trust is stored per exact endpoint. A different port, path or host name is a different endpoint. It is written to durable storage before the device signature is sent.

An identity is never replaced automatically. The user removes it under **Connections → Manage trusted servers**, then connects and approves again.

### 4. `auth_response`

Forge signs the same fields under the label `FORGE-DEVICE-AUTH-V1`. The server verifies the signature with the whitelisted key and rejects an expired challenge.

Every failure reaches the client as the same `auth_failed` message, so it does not reveal which check failed. The whole handshake must finish within 30 seconds.

### 5. Key confirmation

Both sides derive the session keys and exchange an encrypted `key_confirmation`. Only then does the server attach the client to a session and send state.

## Session encryption

Cipher suite: `P256-HKDF-SHA256-AES-256-GCM`.

```text
secret = ECDH-P256(local temporary private key, remote temporary public key)
salt   = SHA-256("FORGE-SESSION-SALT-V1\n" + connectionId + "\n" + clientNonce + "\n" +
                 serverNonce + "\n" + deviceFingerprint + "\n" + agentFingerprint)
prk    = HKDF-Extract-SHA256(salt, secret)

client-to-server key   = HKDF-Expand(prk, "forge-v1 client-to-server key", 32)
server-to-client key   = HKDF-Expand(prk, "forge-v1 server-to-client key", 32)
client nonce prefix    = HKDF-Expand(prk, "forge-v1 client nonce", 4)
server nonce prefix    = HKDF-Expand(prk, "forge-v1 server nonce", 4)
```

Each message:

```json
{ "type": "encrypted", "version": 1, "sequence": 0, "ciphertext": "base64url" }
```

| Part | Value |
|---|---|
| Nonce | 4-byte direction prefix, then the sequence as 8 bytes, big-endian |
| Associated data | `FORGE-ENCRYPTED-V1`, connection ID, direction and sequence, newline separated |
| Sequence | Starts at zero in each direction; the receiver requires exactly the next value |

The two directions use different keys, so a message cannot be reflected to its sender. Once encryption begins, a plaintext message is an error and ends the connection.

## Setting it up

1. In Forge, choose **Copy device whitelist entry**.
2. Add it to `~/.pi-go/authorized-devices.json` on the agent's machine.
3. Start the agent.
4. Connect from Forge and compare the fingerprint with `./pi-go-agent --print-identity`.
5. Choose **Trust this server**.

```bash
./pi-go-agent --listen ws://192.168.1.20:7346/ws --allow-remote --cwd "$PWD"
```

## Rotating keys

Rotation is manual.

| Replace | Steps |
|---|---|
| Agent identity | Remove the identity file, restart, remove the old trusted identity in Forge, reconnect and approve. Devices must be paired for notifications again |
| Device identity | Copy the new whitelist entry, replace the old one, restart the agent |

## Tests

| File | Covers |
|---|---|
| `cmd/pi-go-agent/auth_protocol_test.go` | The complete handshake, and whitelist files that must be rejected |
| `cmd/pi-go-agent/session_crypto_test.go` | Tampered, replayed and wrong-direction messages |
| `flutter/pi_go_app/test/session_crypto_web_test.dart` | The client side of the session cipher |
| `flutter/pi_go_app/test/widget_test.dart` | Stored server trust surviving a restart |

## Possible improvements

1. Pair with a QR code that carries the server fingerprint, removing the reliance on trust on first use.
2. Reload the whitelist without a restart and close the connections of removed devices.
3. Bind iOS and macOS device keys to the Secure Enclave.
4. Encrypt the device identity during the handshake.
5. Verify the handshake formally, for example with Tamarin or ProVerif.

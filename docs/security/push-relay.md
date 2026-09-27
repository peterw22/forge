# The push relay

Phones suspend apps, and a browser closes its pages, so a connection to the agent cannot stay open. To tell you that a tool needs approval or that a turn has finished, a notification has to pass through Apple, Google or the push service of a browser, and through a relay that holds the credentials for those services.

The relay in `worker-push/` is built so that its operator cannot read what it delivers.

## Who holds what

| Party | Holds | Never receives |
|---|---|---|
| Device | Its identity key, the content keys of paired agents | — |
| Agent | Its identity key, the content keys of paired devices | The device's push token |
| Relay | Apple and Google credentials, the Web Push key, encrypted push tokens and subscriptions, who is paired with whom | Identity private keys, content keys, notification text |
| Apple, Google, the push service of a browser | The push token or the subscription | Notification text |

Three kinds of key are involved, and no party holds all of them:

1. **Device identity key**, kept on the device.
2. **Agent identity key**, kept on the agent's machine.
3. **Push service credentials**, stored only as secrets of the relay.

## What the operator can and cannot see

**Cannot see:** the title, the summary, the kind of event, and the session it belongs to. These are encrypted by the agent with a key the relay never receives. In its database the relay records the type and the session as the literal word `encrypted`.

**Can see:**

| Visible | Notes |
|---|---|
| Which agent notified which device, and when | Kept for 30 days |
| Display names of devices and agents | Optional, chosen by the client |
| Push tokens, and the subscriptions of browsers | Encrypted at rest with a key the relay holds |
| The size of each message | Not padded |
| Network addresses of agents and devices | As for any server |

Apple, Google and the push service of a browser see similar metadata for the notifications they carry.

## Content encryption

```text
agent                          relay                     device
  |                              |                          |
  |  content key, signed --------+------------------------->|  over the encrypted
  |                              |                          |  agent connection
  |  encrypt(title, summary,     |                          |
  |          type, session)      |                          |
  |  --- ciphertext ------------>|  --- ciphertext -------->|  decrypt, display
```

- The agent generates a random AES-256 key for each device it is paired with.
- The key travels to the device over the [authenticated, encrypted agent connection](device-authentication.md), never through the relay. It is signed by the agent's identity key over `FORGE-PUSH-KEY-V1`, the agent ID, the device ID, the key ID and the key. The device verifies the signature before it stores the key.
- Each notification is encrypted with AES-256-GCM and a random 12-byte nonce.
- The associated data is `FORGE-PUSH-CONTENT-V1`, the agent ID, the device ID, the event ID and the key ID. A ciphertext that is altered, or delivered for another device or event, fails to decrypt.

The plaintext contains only an event type, a session ID, a fixed title and a one-sentence summary. The summary is written by the [safety gate](safety-gate.md), which is instructed to leave out commands, secrets and paths that contain user names. The agent checks its length and form, and substitutes a fixed sentence when it does not pass.

### On each platform

| Platform | Delivery | Decryption |
|---|---|---|
| iOS | Alert that reads "Encrypted notification", marked as modifiable | A notification service extension decrypts it and replaces the text |
| Android | Data-only message | A native messaging service decrypts it and posts the notification |
| macOS | Background message | Forge decrypts it while it is running |
| Web | Web Push message | The service worker decrypts it and shows the notification |

On Android, on iOS and in a browser no Dart code runs to handle a push. In the apps nothing is shown for an unknown key or a message that fails to decrypt.

### In a browser

Web Push encrypts every message for the browser it is sent to (RFC 8291), with keys of the subscription, which the relay holds. That keeps the message from the push service, not from the relay. The content is therefore encrypted by the agent as on every other platform, and the relay wraps ciphertext.

The relay signs its requests with one key of its own (VAPID, RFC 8292), whose public half a browser is subscribed with. Forge fetches it from the relay. A browser asks for no signature of the site, so a web client that is hosted elsewhere is notified by the same relay.

The relay sends a request to the address a subscription names. It stores a subscription only if that address belongs to the push service of Google, Mozilla, Apple or Microsoft.

A browser withdraws the subscription of a site that receives a push and shows nothing. Two rules of the apps therefore do not hold in a browser:

- A message with an unknown key, or one that fails to decrypt, is shown as "Encrypted notification", with the reason: the browser has no key for it, its key could not be read, or it could not be decrypted.
- A notification is shown for the session on screen too.

The content key is kept in the browser's database as bytes, and no hardware protects it. A script of the site could read it, where it could only use a key object that cannot be exported. Such an object cannot be kept: a browser wraps it when it is stored, Safari with a key of the keychain that iOS releases only while the iPhone is unlocked, so that no notification could be read on a locked iPhone. The key protects one sentence for each notification, and the policy of the page allows no script of another origin.

## Authenticating to the relay

Devices and agents register a public key and prove that they hold the private key by signing a challenge. An identity that is registered cannot be replaced by another key.

Every later request carries:

```text
X-Forge-Principal-Type: device | agent
X-Forge-Principal-ID:   <id>
X-Forge-Timestamp:      <Unix seconds>
X-Forge-Nonce:          <random>
X-Forge-Signature:      <signature>
```

over

```text
FORGE-REQUEST-V1
<METHOD>
<path>
<timestamp>
<nonce>
<base64url SHA-256 of the exact body>
```

The relay rejects a timestamp more than 300 seconds off and a nonce it has seen. Signed endpoints take no query parameters.

## Pairing

Registering grants nothing. An agent may notify a device only after that device approves it.

1. Over the agent connection, the agent proves its identity to the device by signing a challenge the device chose.
2. The agent asks the relay to pair with the device. The relay creates a pairing that expires in five minutes and has a six-digit code.
3. The agent sends the pairing ID and the code to the device over the agent connection.
4. Forge fetches the pairing from the relay and shows the agent's name, fingerprint and the code.
5. When the user approves, the device signs the approval and sends it to the relay.
6. The agent provisions the content key over the agent connection.

Only the device can approve, and an agent cannot approve its own pairing. Either side can revoke; revoking takes effect on the next event.

## API

Registration:

- `GET /healthz`
- `GET /v1/web-push-key`
- `POST /v1/registration-challenges`
- `POST /v1/registrations`

Signed by a device:

- `PUT`, `DELETE /v1/devices/{deviceId}/push-endpoint`
- `GET /v1/devices/{deviceId}/pairings/{pairingId}`
- `POST /v1/devices/{deviceId}/pairings/{pairingId}/approve`
- `GET /v1/devices/{deviceId}/authorizations`
- `DELETE /v1/devices/{deviceId}/authorizations/{agentId}`

Signed by an agent:

- `POST /v1/agents/{agentId}/pairings`
- `GET /v1/agents/{agentId}/pairings/{pairingId}`
- `GET /v1/agents/{agentId}/authorizations`
- `DELETE /v1/agents/{agentId}/authorizations/{deviceId}`
- `POST /v1/agents/{agentId}/events`

Responses are sent with `Cache-Control: no-store`, and may be read from any origin: a request is authorized by its signature, and the relay sets no cookie. Errors have the form:

```json
{ "error": { "code": "invalid_signature", "message": "Request signature is invalid" } }
```

## Limits and storage

| | |
|---|---|
| Events per agent | 30 per minute by default |
| Repeated event ID | Accepted once; a repeat is acknowledged and not delivered again |
| Event records | Deleted after 30 days by a daily job |
| Challenges, nonces, pairings | Deleted once expired |
| Push tokens | AES-GCM encrypted; a hash enforces uniqueness |
| Rejected tokens | Disabled when Apple, Google or the push service of a browser reports them invalid |

## Limitations

- **One long-lived content key per agent and device.** There is no rotation. If a key leaks, recorded ciphertext of earlier notifications can be read.
- **The relay can drop or delay a notification.** No relay can prevent that.
- **The relay can replay a notification.** Devices do not remember which events they have shown, so a repeated message is displayed again. It cannot be altered or retargeted.
- **Message size is visible.**
- **Delivery is attempted once, within the request.** There is no queue or retry.
- **A browser may replace its subscription.** Forge registers the new one when it is opened next; until then that browser receives nothing.

## Running your own

The agent uses `https://forge-push.tingouw.com` unless `PI_GO_PUSH_RELAY_URL` names another relay. `PI_GO_PUSH_DISABLED=true` turns push off. The client takes its relay from the build setting `FORGE_PUSH_RELAY_URL`; an empty value builds a client without push.

Deployment is described in [`worker-push/README.md`](../../worker-push/README.md). A push service only accepts notifications for apps signed by the account that owns the credentials, so your own relay needs your own build of Forge. For browsers it needs a Web Push key alone.

## Tests

| File | Covers |
|---|---|
| `worker-push/test/crypto.test.ts` | Request signing and signature verification |
| `worker-push/test/webpush.test.ts` | Web Push encryption against the example of RFC 8291, the signature of the relay, which addresses a subscription may name |
| `worker-push/test/relay.test.ts` | A browser from registration to delivery, on a database with every migration applied |
| `flutter/pi_go_app/test/forge_push_test.mjs` | Decryption in the service worker, of an envelope the agent encrypted |
| `flutter/pi_go_app/test/web_push_test.dart` | The content key and the tap on a notification, in a browser |
| `scripts/test-web-push.cjs` | The whole path in Chrome, with a relay and an agent of its own: permission, pairing, a notification for a turn that ended, a tap, and a push that cannot be read |
| `cmd/pi-go-agent/push_content_crypto_test.go` | Content encryption and its binding to agent, device and event |
| `cmd/pi-go-agent/push_authorization_test.go` | Listing and revoking authorizations |

## Related work

- Web Push message encryption (RFC 8291) encrypts a payload so that the push service cannot read it. Forge uses it for browsers, around its own encryption, which keeps the content from the relay as well.
- Signal sends a push that carries no content and fetches the message over its own channel.

Forge carries a short encrypted summary in the push itself, because the app cannot hold a connection to fetch one.

## Possible improvements

1. Pad messages to a fixed size.
2. Rotate content keys.
3. Keep less metadata: shorten retention, or do not record the device of each event.
4. Reject events a device has already shown.
5. Queue deliveries and retry them.

import { arrayBuffer, base64UrlDecode, base64UrlEncode, sha256, utf8 } from "./crypto";
import { ApiError } from "./http";
import type { Env } from "./types";
import type { EncryptedPushEnvelope } from "./fcm";

export interface WebPushSubscription {
  endpoint: string;
  p256dh: string;
  auth: string;
}

export interface WebPushOpaqueEvent {
  eventId: string;
  encrypted: EncryptedPushEnvelope;
}

export interface WebPushResult {
  status: number;
  reason: string | null;
  messageId: string | null;
}

// Push services limit a message to 4096 bytes; the record header and the
// authentication tag take 103 of them.
const maximumPlaintextBytes = 3900;
const recordSize = 4096;

const cachedJwts = new Map<string, { token: string; expiresAt: number }>();

export function webPushConfigured(env: Env): boolean {
  return Boolean(env.VAPID_PUBLIC_KEY) && Boolean(env.VAPID_PRIVATE_KEY) && Boolean(env.VAPID_SUBJECT);
}

// The relay sends a request to the address a browser names, so it accepts only
// the push services of the browser vendors.
export function allowedPushServiceHost(host: string): boolean {
  return host === "fcm.googleapis.com" ||
    host === "updates.push.services.mozilla.com" ||
    host.endsWith(".push.apple.com") ||
    host.endsWith(".notify.windows.com");
}

export function parseSubscription(value: unknown): WebPushSubscription {
  if (typeof value !== "object" || value === null) {
    throw new ApiError(400, "invalid_subscription", "subscription must be a push subscription");
  }
  const body = value as { endpoint?: unknown; keys?: { p256dh?: unknown; auth?: unknown } };
  if (typeof body.endpoint !== "string" || body.endpoint.length > 2048) {
    throw new ApiError(400, "invalid_subscription", "subscription endpoint is invalid");
  }
  let url: URL;
  try {
    url = new URL(body.endpoint);
  } catch {
    throw new ApiError(400, "invalid_subscription", "subscription endpoint is invalid");
  }
  if (url.protocol !== "https:" || url.port !== "" || url.username !== "" || url.password !== "" ||
      !allowedPushServiceHost(url.hostname)) {
    throw new ApiError(400, "unsupported_push_service", "subscription endpoint is not a known push service");
  }
  const p256dh = body.keys?.p256dh;
  const auth = body.keys?.auth;
  if (typeof p256dh !== "string" || typeof auth !== "string") {
    throw new ApiError(400, "invalid_subscription", "subscription keys are missing");
  }
  try {
    const point = base64UrlDecode(p256dh);
    if (point.length !== 65 || point[0] !== 4 || base64UrlDecode(auth).length !== 16) throw new Error("length");
  } catch {
    throw new ApiError(400, "invalid_subscription", "subscription keys are invalid");
  }
  return { endpoint: url.href, p256dh, auth };
}

export function webPushMessage(event: WebPushOpaqueEvent): Record<string, unknown> {
  return {
    version: event.encrypted.version,
    eventId: event.eventId,
    keyId: event.encrypted.keyId,
    nonce: event.encrypted.nonce,
    ciphertext: event.encrypted.ciphertext,
  };
}

export async function sendWebPush(
  env: Env,
  subscription: WebPushSubscription,
  event: WebPushOpaqueEvent,
): Promise<WebPushResult> {
  if (!webPushConfigured(env)) {
    throw new ApiError(503, "web_push_unavailable", "This relay has no Web Push key");
  }
  const target = parseSubscription({
    endpoint: subscription.endpoint,
    keys: { p256dh: subscription.p256dh, auth: subscription.auth },
  });
  const plaintext = utf8(JSON.stringify(webPushMessage(event)));
  if (plaintext.length > maximumPlaintextBytes) {
    throw new ApiError(413, "event_too_large", "The notification is too large for Web Push");
  }
  const body = await encryptWebPush(plaintext, target);
  const topic = base64UrlEncode(await sha256(event.eventId)).slice(0, 32);
  const response = await fetch(target.endpoint, {
    method: "POST",
    headers: {
      authorization: await vapidAuthorization(env, new URL(target.endpoint).origin),
      "content-encoding": "aes128gcm",
      "content-type": "application/octet-stream",
      ttl: "86400",
      urgency: "high",
      topic,
    },
    body: arrayBuffer(body),
  });
  let reason: string | null = null;
  if (!response.ok) {
    try {
      reason = (await response.text()).slice(0, 200) || "UnknownWebPushError";
    } catch { reason = "InvalidWebPushResponse"; }
  }
  return { status: response.status, reason, messageId: response.headers.get("location") };
}

// Encrypts a message for one subscription as RFC 8291 describes. The sender
// key and the salt are chosen at random; tests pass those of the RFC.
export async function encryptWebPush(
  plaintext: Uint8Array,
  subscription: Pick<WebPushSubscription, "p256dh" | "auth">,
  fixed?: { senderPrivateKey: JsonWebKey; salt: Uint8Array },
): Promise<Uint8Array> {
  const receiverPublic = base64UrlDecode(subscription.p256dh);
  const receiverKey = await crypto.subtle.importKey(
    "raw", arrayBuffer(receiverPublic), { name: "ECDH", namedCurve: "P-256" }, false, [],
  );
  let senderPrivate: CryptoKey;
  let senderPublic: Uint8Array;
  if (fixed === undefined) {
    const pair = await crypto.subtle.generateKey(
      { name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"],
    ) as CryptoKeyPair;
    senderPrivate = pair.privateKey;
    senderPublic = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey) as ArrayBuffer);
  } else {
    senderPrivate = await crypto.subtle.importKey(
      "jwk", fixed.senderPrivateKey, { name: "ECDH", namedCurve: "P-256" }, false, ["deriveBits"],
    );
    senderPublic = uncompressedPoint(fixed.senderPrivateKey);
  }
  const salt = fixed?.salt ?? crypto.getRandomValues(new Uint8Array(16));
  const shared = new Uint8Array(await crypto.subtle.deriveBits(
    { name: "ECDH", public: receiverKey }, senderPrivate, 256,
  ));
  const keyInfo = concatenate(utf8("WebPush: info\0"), receiverPublic, senderPublic);
  const material = await hkdf(shared, base64UrlDecode(subscription.auth), keyInfo, 32);
  const contentKey = await hkdf(material, salt, utf8("Content-Encoding: aes128gcm\0"), 16);
  const nonce = await hkdf(material, salt, utf8("Content-Encoding: nonce\0"), 12);
  const key = await crypto.subtle.importKey("raw", arrayBuffer(contentKey), "AES-GCM", false, ["encrypt"]);
  // One record holds the whole message; 0x02 ends the last record.
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: arrayBuffer(nonce) }, key, arrayBuffer(concatenate(plaintext, Uint8Array.of(2))),
  ));
  const header = new Uint8Array(21);
  header.set(salt, 0);
  new DataView(header.buffer).setUint32(16, recordSize);
  header[20] = senderPublic.length;
  return concatenate(header, senderPublic, ciphertext);
}

export async function vapidAuthorization(env: Env, audience: string): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  const cached = cachedJwts.get(audience);
  if (cached !== undefined && cached.expiresAt > now + 60) {
    return `vapid t=${cached.token}, k=${env.VAPID_PUBLIC_KEY}`;
  }
  const expiresAt = now + 12 * 60 * 60;
  const header = base64UrlEncode(utf8(JSON.stringify({ typ: "JWT", alg: "ES256" })));
  const claims = base64UrlEncode(utf8(JSON.stringify({ aud: audience, exp: expiresAt, sub: env.VAPID_SUBJECT })));
  const signingInput = `${header}.${claims}`;
  const key = await importVapidKey(env);
  const signature = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, arrayBuffer(utf8(signingInput)));
  const token = `${signingInput}.${base64UrlEncode(signature)}`;
  cachedJwts.set(audience, { token, expiresAt });
  return `vapid t=${token}, k=${env.VAPID_PUBLIC_KEY}`;
}

async function importVapidKey(env: Env): Promise<CryptoKey> {
  const point = base64UrlDecode(env.VAPID_PUBLIC_KEY);
  const secret = base64UrlDecode(env.VAPID_PRIVATE_KEY.trim());
  if (point.length !== 65 || point[0] !== 4) throw new Error("VAPID_PUBLIC_KEY is not an uncompressed P-256 point");
  if (secret.length !== 32) throw new Error("VAPID_PRIVATE_KEY must contain 32 bytes");
  return crypto.subtle.importKey(
    "jwk",
    {
      kty: "EC",
      crv: "P-256",
      x: base64UrlEncode(point.slice(1, 33)),
      y: base64UrlEncode(point.slice(33)),
      d: base64UrlEncode(secret),
    },
    { name: "ECDSA", namedCurve: "P-256" },
    false,
    ["sign"],
  );
}

async function hkdf(material: Uint8Array, salt: Uint8Array, info: Uint8Array, length: number): Promise<Uint8Array> {
  const key = await crypto.subtle.importKey("raw", arrayBuffer(material), "HKDF", false, ["deriveBits"]);
  return new Uint8Array(await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: arrayBuffer(salt), info: arrayBuffer(info) }, key, length * 8,
  ));
}

function uncompressedPoint(jwk: JsonWebKey): Uint8Array {
  if (typeof jwk.x !== "string" || typeof jwk.y !== "string") throw new Error("key has no public point");
  return concatenate(Uint8Array.of(4), base64UrlDecode(jwk.x), base64UrlDecode(jwk.y));
}

function concatenate(...parts: Uint8Array[]): Uint8Array {
  const result = new Uint8Array(parts.reduce((length, part) => length + part.length, 0));
  let offset = 0;
  for (const part of parts) {
    result.set(part, offset);
    offset += part.length;
  }
  return result;
}

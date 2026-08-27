import { arrayBuffer, base64UrlDecode, base64UrlEncode, utf8 } from "./crypto";
import { ApiError } from "./http";
import type { Env } from "./types";
import type { EncryptedPushEnvelope } from "./fcm";

let cachedJwt: { token: string; expiresAt: number } | null = null;

export interface ApnsOpaqueEvent {
  eventId: string;
  encrypted: EncryptedPushEnvelope;
}

export interface ApnsResult {
  status: number;
  reason: string | null;
  apnsId: string | null;
}

export function notificationPayload(event: ApnsOpaqueEvent, background = false): Record<string, unknown> {
  return {
    aps: background
      ? { "content-available": 1 }
      : {
          alert: { title: "Forge", body: "Encrypted notification" },
          sound: "default",
          "mutable-content": 1,
        },
    version: event.encrypted.version,
    eventId: event.eventId,
    keyId: event.encrypted.keyId,
    nonce: event.encrypted.nonce,
    ciphertext: event.encrypted.ciphertext,
  };
}

export async function sendApns(
  env: Env,
  endpoint: { token: string; environment: "development" | "production"; topic: string },
  event: ApnsOpaqueEvent,
  background = false,
): Promise<ApnsResult> {
  const topic = endpoint.topic || env.APNS_TOPIC;
  if (topic !== env.APNS_TOPIC) throw new ApiError(400, "invalid_topic", "APNs topic is not allowed");
  if (!/^[0-9a-fA-F]{32,256}$/u.test(endpoint.token)) {
    throw new ApiError(400, "invalid_push_token", "APNs device token is invalid");
  }
  const jwt = await apnsProviderJwt(env);
  const host = endpoint.environment === "production" ? "api.push.apple.com" : "api.sandbox.push.apple.com";
  const collapseDigest = await crypto.subtle.digest("SHA-256", arrayBuffer(utf8(event.eventId)));
  const response = await fetch(`https://${host}/3/device/${endpoint.token}`, {
    method: "POST",
    headers: {
      authorization: `bearer ${jwt}`,
      "content-type": "application/json",
      "apns-topic": topic,
      "apns-push-type": background ? "background" : "alert",
      "apns-priority": background ? "5" : "10",
      "apns-collapse-id": base64UrlEncode(collapseDigest).slice(0, 64),
    },
    body: JSON.stringify(notificationPayload(event, background)),
  });
  let reason: string | null = null;
  if (!response.ok) {
    try {
      const body = (await response.json()) as { reason?: unknown };
      reason = typeof body.reason === "string" ? body.reason : "UnknownAPNSError";
    } catch { reason = "InvalidAPNsResponse"; }
  }
  return { status: response.status, reason, apnsId: response.headers.get("apns-id") };
}

async function apnsProviderJwt(env: Env): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  if (cachedJwt !== null && cachedJwt.expiresAt > now + 60) return cachedJwt.token;
  const header = base64UrlEncode(utf8(JSON.stringify({ alg: "ES256", kid: env.APNS_KEY_ID })));
  const claims = base64UrlEncode(utf8(JSON.stringify({ iss: env.APNS_TEAM_ID, iat: now })));
  const signingInput = `${header}.${claims}`;
  const key = await importApnsKey(env.APNS_PRIVATE_KEY_P8);
  const signature = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, arrayBuffer(utf8(signingInput)));
  const token = `${signingInput}.${base64UrlEncode(signature)}`;
  cachedJwt = { token, expiresAt: now + 50 * 60 };
  return token;
}

async function importApnsKey(pem: string): Promise<CryptoKey> {
  const normalized = pem.replace(/\\n/g, "\n");
  const match = /-----BEGIN PRIVATE KEY-----([\s\S]+?)-----END PRIVATE KEY-----/u.exec(normalized);
  if (match?.[1] === undefined) throw new Error("APNS_PRIVATE_KEY_P8 is not a PKCS#8 private key");
  const der = base64UrlDecode(match[1].replace(/\s+/gu, "").replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, ""));
  return crypto.subtle.importKey("pkcs8", arrayBuffer(der), { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
}

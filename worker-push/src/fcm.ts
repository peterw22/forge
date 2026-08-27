import { ApiError } from "./http";
import type { Env } from "./types";

interface ServiceAccount {
  type: string;
  project_id: string;
  private_key: string;
  client_email: string;
}

export interface EncryptedPushEnvelope {
  version: 1;
  keyId: string;
  nonce: string;
  ciphertext: string;
}

export interface FcmOpaqueEvent {
  eventId: string;
  encrypted: EncryptedPushEnvelope;
}

export interface FcmResult {
  status: number;
  reason: string | null;
  messageId: string | null;
}

let cachedAccessToken: { token: string; expiresAt: number; projectId: string } | null = null;

export function fcmMessage(token: string, event: FcmOpaqueEvent): Record<string, unknown> {
  return {
    message: {
      token,
      data: {
        version: String(event.encrypted.version),
        eventId: event.eventId,
        keyId: event.encrypted.keyId,
        nonce: event.encrypted.nonce,
        ciphertext: event.encrypted.ciphertext,
      },
      android: {
        priority: "high",
        ttl: "86400s",
      },
    },
  };
}

export async function sendFcm(env: Env, token: string, event: FcmOpaqueEvent): Promise<FcmResult> {
  if (token.length < 20 || token.length > 4096 || /\s/u.test(token)) {
    throw new ApiError(400, "invalid_push_token", "FCM registration token is invalid");
  }
  const account = serviceAccount(env);
  if (account.project_id !== env.FCM_PROJECT_ID) {
    throw new Error("FCM service account project does not match FCM_PROJECT_ID");
  }
  const accessToken = await googleAccessToken(account);
  const response = await fetch(
    `https://fcm.googleapis.com/v1/projects/${encodeURIComponent(env.FCM_PROJECT_ID)}/messages:send`,
    {
      method: "POST",
      headers: {
        authorization: `Bearer ${accessToken}`,
        "content-type": "application/json",
      },
      body: JSON.stringify(fcmMessage(token, event)),
    },
  );
  let messageId: string | null = null;
  let reason: string | null = null;
  try {
    const body = await response.json() as {
      name?: unknown;
      error?: { status?: unknown; message?: unknown; details?: Array<{ errorCode?: unknown }> };
    };
    if (response.ok) {
      messageId = typeof body.name === "string" ? body.name : null;
    } else {
      const detailCode = body.error?.details?.find((detail) => typeof detail.errorCode === "string")?.errorCode;
      reason = typeof detailCode === "string"
        ? detailCode
        : typeof body.error?.status === "string"
          ? body.error.status
          : typeof body.error?.message === "string"
            ? body.error.message.slice(0, 200)
            : "UnknownFCMError";
    }
  } catch {
    reason = response.ok ? null : "InvalidFCMResponse";
  }
  return { status: response.status, reason, messageId };
}

function serviceAccount(env: Env): ServiceAccount {
  let parsed: unknown;
  try {
    parsed = JSON.parse(env.FCM_SERVICE_ACCOUNT_JSON);
  } catch {
    throw new Error("FCM_SERVICE_ACCOUNT_JSON is not valid JSON");
  }
  const value = parsed as Partial<ServiceAccount>;
  if (
    value.type !== "service_account" ||
    typeof value.project_id !== "string" ||
    typeof value.client_email !== "string" ||
    typeof value.private_key !== "string"
  ) {
    throw new Error("FCM_SERVICE_ACCOUNT_JSON is not a valid service account");
  }
  return value as ServiceAccount;
}

async function googleAccessToken(account: ServiceAccount): Promise<string> {
  const now = Math.floor(Date.now() / 1000);
  if (
    cachedAccessToken !== null &&
    cachedAccessToken.projectId === account.project_id &&
    cachedAccessToken.expiresAt > now + 60
  ) {
    return cachedAccessToken.token;
  }
  const header = base64UrlEncode(new TextEncoder().encode(JSON.stringify({ alg: "RS256", typ: "JWT" })));
  const claims = base64UrlEncode(new TextEncoder().encode(JSON.stringify({
    iss: account.client_email,
    scope: "https://www.googleapis.com/auth/firebase.messaging",
    aud: "https://oauth2.googleapis.com/token",
    iat: now,
    exp: now + 3600,
  })));
  const signingInput = `${header}.${claims}`;
  const key = await importServiceAccountKey(account.private_key);
  const signature = await crypto.subtle.sign(
    { name: "RSASSA-PKCS1-v1_5" }, key, new TextEncoder().encode(signingInput),
  );
  const assertion = `${signingInput}.${base64UrlEncode(new Uint8Array(signature))}`;
  const response = await fetch("https://oauth2.googleapis.com/token", {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "urn:ietf:params:oauth:grant-type:jwt-bearer", assertion,
    }).toString(),
  });
  const body = await response.json() as { access_token?: unknown; expires_in?: unknown; error_description?: unknown };
  if (!response.ok || typeof body.access_token !== "string") {
    throw new Error(`FCM OAuth token exchange failed: ${String(body.error_description ?? response.status)}`);
  }
  const expiresIn = typeof body.expires_in === "number" ? body.expires_in : 3600;
  cachedAccessToken = { token: body.access_token, expiresAt: now + Math.max(60, expiresIn), projectId: account.project_id };
  return body.access_token;
}

async function importServiceAccountKey(pem: string): Promise<CryptoKey> {
  const normalized = pem.replace(/\\n/g, "\n");
  const match = /-----BEGIN PRIVATE KEY-----([\s\S]+?)-----END PRIVATE KEY-----/u.exec(normalized);
  if (match?.[1] === undefined) throw new Error("FCM service account key is not PKCS#8");
  const binary = atob(match[1].replace(/\s+/gu, ""));
  const der = Uint8Array.from(binary, (value) => value.charCodeAt(0));
  return crypto.subtle.importKey(
    "pkcs8", der, { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" }, false, ["sign"],
  );
}

function base64UrlEncode(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

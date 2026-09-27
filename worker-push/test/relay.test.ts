import { applyD1Migrations, env } from "cloudflare:test";
import type { D1Migration } from "cloudflare:test";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { base64UrlEncode, requestSigningPayload } from "../src/crypto";
import worker from "../src/index";
import type { Env, PublicJwk } from "../src/types";
import { decryptAsReceiver } from "./receiver";

interface Principal {
  type: "device" | "agent";
  id: string;
  keys: CryptoKeyPair;
  publicKey: PublicJwk;
}

const relay = "https://relay.example";
let relayEnv: Env;

async function sign(principal: Principal, payload: string): Promise<string> {
  return base64UrlEncode(await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" }, principal.keys.privateKey, new TextEncoder().encode(payload),
  ));
}

async function post(path: string, body: unknown): Promise<Response> {
  return worker.fetch(new Request(`${relay}${path}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  }), relayEnv);
}

async function register(type: "device" | "agent", id: string, platform?: string): Promise<Principal> {
  const keys = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"],
  ) as CryptoKeyPair;
  const exported = await crypto.subtle.exportKey("jwk", keys.publicKey) as JsonWebKey;
  const principal: Principal = {
    type, id, keys, publicKey: { kty: "EC", crv: "P-256", x: exported.x!, y: exported.y! },
  };
  const challenge = await post("/v1/registration-challenges", {
    principalType: type, principalId: id, platform, publicKey: principal.publicKey,
  });
  expect(challenge.status).toBe(201);
  const { challengeId, signingPayload } = await challenge.json() as { challengeId: string; signingPayload: string };
  const completed = await post("/v1/registrations", { challengeId, signature: await sign(principal, signingPayload) });
  expect(completed.status).toBe(200);
  return principal;
}

async function signed(principal: Principal, method: string, path: string, body: unknown): Promise<Response> {
  const bytes = new TextEncoder().encode(JSON.stringify(body));
  const timestamp = String(Math.floor(Date.now() / 1000));
  const nonce = base64UrlEncode(crypto.getRandomValues(new Uint8Array(18)));
  const payload = await requestSigningPayload(method, path, timestamp, nonce, bytes);
  return worker.fetch(new Request(`${relay}${path}`, {
    method,
    headers: {
      "content-type": "application/json",
      "X-Forge-Principal-Type": principal.type,
      "X-Forge-Principal-ID": principal.id,
      "X-Forge-Timestamp": timestamp,
      "X-Forge-Nonce": nonce,
      "X-Forge-Signature": await sign(principal, payload),
    },
    body: bytes,
  }), relayEnv);
}

async function subscriptionOf(endpoint: string) {
  const keys = await crypto.subtle.generateKey(
    { name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"],
  ) as CryptoKeyPair;
  const auth = crypto.getRandomValues(new Uint8Array(16));
  return {
    keys,
    auth,
    json: {
      endpoint,
      keys: {
        p256dh: base64UrlEncode(await crypto.subtle.exportKey("raw", keys.publicKey) as ArrayBuffer),
        auth: base64UrlEncode(auth),
      },
    },
  };
}

const endpointBody = (subscription: unknown) => ({
  provider: "webpush", environment: "production", topic: relayEnv.APNS_TOPIC, subscription,
});

async function authorize(device: Principal, agent: Principal): Promise<void> {
  const now = Math.floor(Date.now() / 1000);
  await relayEnv.DB.prepare(
    "INSERT INTO authorizations (id, device_id, agent_id, scopes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
  ).bind(`authorization-${device.id}`, device.id, agent.id, '["notify.approval","notify.completed"]', now, now).run();
}

const event = (device: Principal, eventId: string) => ({
  deviceId: device.id,
  eventId,
  encrypted: {
    version: 1, keyId: "opaque-key-id-12345", nonce: "AAAAAAAAAAAAAAAA", ciphertext: "opaque-ciphertext-value-0",
  },
});

beforeAll(async () => {
  const signing = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"],
  ) as CryptoKeyPair;
  const secret = await crypto.subtle.exportKey("jwk", signing.privateKey) as JsonWebKey;
  const provided = env as unknown as Env & { TEST_MIGRATIONS: D1Migration[] };
  relayEnv = {
    ...provided,
    PUSH_TOKEN_ENCRYPTION_KEY: base64UrlEncode(crypto.getRandomValues(new Uint8Array(32))),
    VAPID_PUBLIC_KEY: base64UrlEncode(await crypto.subtle.exportKey("raw", signing.publicKey) as ArrayBuffer),
    VAPID_PRIVATE_KEY: secret.d!,
    VAPID_SUBJECT: "https://forge.example",
  };
  await applyD1Migrations(provided.DB, provided.TEST_MIGRATIONS);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("A browser as a device", () => {
  it("receives an event through its push service, which cannot read it", async () => {
    const device = await register("device", "web-device-identifier-01", "web");
    const agent = await register("agent", "agent-identifier-000001");
    const subscription = await subscriptionOf("https://fcm.googleapis.com/fcm/send/subscription-1");
    const stored = await signed(
      device, "PUT", `/v1/devices/${device.id}/push-endpoint`, endpointBody(subscription.json),
    );
    expect(stored.status).toBe(200);
    expect(await stored.json()).toMatchObject({ registered: true, provider: "webpush" });
    const row = await relayEnv.DB.prepare("SELECT provider, token_ciphertext FROM push_endpoints WHERE device_id = ?")
      .bind(device.id).first<{ provider: string; token_ciphertext: string }>();
    expect(row?.provider).toBe("webpush");
    expect(row?.token_ciphertext).not.toContain("subscription-1");
    await authorize(device, agent);

    const sent: Request[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      sent.push(new Request(input as RequestInfo, init));
      return new Response(null, { status: 201, headers: { location: "https://fcm.googleapis.com/message/1" } });
    });
    const response = await signed(
      agent, "POST", `/v1/agents/${agent.id}/events`, event(device, "event-identifier-0001"),
    );
    expect(response.status).toBe(202);
    expect(await response.json()).toMatchObject({ delivered: true, provider: "webpush" });

    expect(sent).toHaveLength(1);
    const request = sent[0]!;
    expect(request.url).toBe("https://fcm.googleapis.com/fcm/send/subscription-1");
    expect(request.method).toBe("POST");
    expect(request.headers.get("content-encoding")).toBe("aes128gcm");
    expect(request.headers.get("ttl")).toBe("86400");
    expect(request.headers.get("authorization")).toMatch(
      new RegExp(`^vapid t=[^,]+, k=${relayEnv.VAPID_PUBLIC_KEY}$`, "u"),
    );
    const body = new Uint8Array(await request.arrayBuffer());
    expect(body.length).toBeLessThanOrEqual(4096);
    const message = JSON.parse(await decryptAsReceiver(body, subscription.keys, subscription.auth)) as unknown;
    expect(message).toEqual({
      version: 1, eventId: "event-identifier-0001", keyId: "opaque-key-id-12345",
      nonce: "AAAAAAAAAAAAAAAA", ciphertext: "opaque-ciphertext-value-0",
    });
  });

  it("loses a subscription that its push service no longer knows", async () => {
    const device = await register("device", "web-device-identifier-02", "web");
    const agent = await register("agent", "agent-identifier-000002");
    const subscription = await subscriptionOf("https://updates.push.services.mozilla.com/wpush/v2/subscription-2");
    await signed(device, "PUT", `/v1/devices/${device.id}/push-endpoint`, endpointBody(subscription.json));
    await authorize(device, agent);
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response("gone", { status: 410 }));
    const response = await signed(
      agent, "POST", `/v1/agents/${agent.id}/events`, event(device, "event-identifier-0002"),
    );
    expect(response.status).toBe(502);
    const row = await relayEnv.DB.prepare("SELECT disabled_at FROM push_endpoints WHERE device_id = ?")
      .bind(device.id).first<{ disabled_at: number | null }>();
    expect(row?.disabled_at).not.toBeNull();
    vi.restoreAllMocks();
    const again = await signed(
      agent, "POST", `/v1/agents/${agent.id}/events`, event(device, "event-identifier-0003"),
    );
    expect(again.status).toBe(409);
  });

  it("stores no address but that of a push service, and no token of an app", async () => {
    const device = await register("device", "web-device-identifier-03", "web");
    const path = `/v1/devices/${device.id}/push-endpoint`;
    const elsewhere = await subscriptionOf("https://relay.example/v1/agents");
    const refused = await signed(device, "PUT", path, endpointBody(elsewhere.json));
    expect(refused.status).toBe(400);
    expect(await refused.json()).toMatchObject({ error: { code: "unsupported_push_service" } });
    const token = await signed(device, "PUT", path, {
      provider: "apns", environment: "production", topic: relayEnv.APNS_TOPIC, token: "ab".repeat(32),
    });
    expect(token.status).toBe(400);
    expect(await token.json()).toMatchObject({ error: { code: "provider_platform_mismatch" } });
    const count = await relayEnv.DB.prepare("SELECT count(*) AS count FROM push_endpoints WHERE device_id = ?")
      .bind(device.id).first<{ count: number }>();
    expect(count?.count).toBe(0);
  });

  it("is refused where the relay has no Web Push key", async () => {
    const device = await register("device", "web-device-identifier-04", "web");
    const subscription = await subscriptionOf("https://web.push.apple.com/subscription-4");
    const complete = relayEnv;
    relayEnv = { ...complete, VAPID_PRIVATE_KEY: "" };
    try {
      const response = await signed(
        device, "PUT", `/v1/devices/${device.id}/push-endpoint`, endpointBody(subscription.json),
      );
      expect(response.status).toBe(503);
    } finally {
      relayEnv = complete;
    }
  });
});

describe("An app as a device", () => {
  it("still registers its token", async () => {
    const device = await register("device", "ios-device-identifier-01", "ios");
    const response = await signed(device, "PUT", `/v1/devices/${device.id}/push-endpoint`, {
      provider: "apns", environment: "production", topic: relayEnv.APNS_TOPIC, token: "ab".repeat(32),
    });
    expect(response.status).toBe(200);
    const web = await signed(device, "PUT", `/v1/devices/${device.id}/push-endpoint`, endpointBody({}));
    expect(web.status).toBe(400);
  });
});

import { describe, expect, it } from "vitest";
import { base64UrlDecode, base64UrlEncode } from "../src/crypto";
import {
  allowedPushServiceHost,
  encryptWebPush,
  parseSubscription,
  vapidAuthorization,
  webPushMessage,
} from "../src/webpush";
import worker from "../src/index";
import { decryptAsReceiver } from "./receiver";
import type { Env } from "../src/types";

const bytes = (value: Uint8Array): ArrayBuffer => Uint8Array.from(value).buffer;

describe("Web Push encryption", () => {
  it("produces the message of RFC 8291, section 5", async () => {
    const senderPublic = base64UrlDecode(
      "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8",
    );
    const body = await encryptWebPush(
      new TextEncoder().encode("When I grow up, I want to be a watermelon"),
      {
        p256dh: "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
        auth: "BTBZMqHH6r4Tts7J_aSIgg",
      },
      {
        senderPrivateKey: {
          kty: "EC",
          crv: "P-256",
          x: base64UrlEncode(senderPublic.slice(1, 33)),
          y: base64UrlEncode(senderPublic.slice(33)),
          d: "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw",
        },
        salt: base64UrlDecode("DGv6ra1nlYgDCS1FRnbzlw"),
      },
    );
    expect(base64UrlEncode(body)).toBe(
      "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
      "mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
      "pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN",
    );
  });

  it("is read by the subscription it was written for, and by no other", async () => {
    const receiver = await crypto.subtle.generateKey(
      { name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"],
    ) as CryptoKeyPair;
    const auth = crypto.getRandomValues(new Uint8Array(16));
    const p256dh = base64UrlEncode(await crypto.subtle.exportKey("raw", receiver.publicKey) as ArrayBuffer);
    const message = JSON.stringify({ version: 1, eventId: "opaque-event-id-12345" });
    const body = await encryptWebPush(
      new TextEncoder().encode(message), { p256dh, auth: base64UrlEncode(auth) },
    );
    expect(await decryptAsReceiver(body, receiver, auth)).toBe(message);
    const again = await encryptWebPush(
      new TextEncoder().encode(message), { p256dh, auth: base64UrlEncode(auth) },
    );
    expect(base64UrlEncode(again)).not.toBe(base64UrlEncode(body));
    const other = crypto.getRandomValues(new Uint8Array(16));
    await expect(decryptAsReceiver(body, receiver, other)).rejects.toThrow();
  });
});

describe("VAPID", () => {
  it("signs a token for the push service, which the public key verifies", async () => {
    const pair = await crypto.subtle.generateKey(
      { name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"],
    ) as CryptoKeyPair;
    const jwk = await crypto.subtle.exportKey("jwk", pair.privateKey) as JsonWebKey;
    const publicKey = base64UrlEncode(await crypto.subtle.exportKey("raw", pair.publicKey) as ArrayBuffer);
    const env = {
      VAPID_PUBLIC_KEY: publicKey,
      VAPID_PRIVATE_KEY: jwk.d!,
      VAPID_SUBJECT: "https://forge.example",
    } as Env;
    const header = await vapidAuthorization(env, "https://fcm.googleapis.com");
    const match = /^vapid t=([^,]+), k=(.+)$/u.exec(header);
    expect(match?.[2]).toBe(publicKey);
    const [head, claims, signature] = match![1]!.split(".");
    expect(JSON.parse(new TextDecoder().decode(base64UrlDecode(head!)))).toEqual({ typ: "JWT", alg: "ES256" });
    const body = JSON.parse(new TextDecoder().decode(base64UrlDecode(claims!))) as Record<string, unknown>;
    expect(body.aud).toBe("https://fcm.googleapis.com");
    expect(body.sub).toBe("https://forge.example");
    const now = Math.floor(Date.now() / 1000);
    expect(body.exp).toBeGreaterThan(now);
    expect(body.exp).toBeLessThanOrEqual(now + 24 * 60 * 60);
    expect(await crypto.subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      pair.publicKey,
      bytes(base64UrlDecode(signature!)),
      new TextEncoder().encode(`${head}.${claims}`),
    )).toBe(true);
  });
});

describe("Web Push subscriptions", () => {
  const keys = {
    p256dh: "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
    auth: "BTBZMqHH6r4Tts7J_aSIgg",
  };

  it("accepts the push services of the browser vendors", () => {
    for (const endpoint of [
      "https://fcm.googleapis.com/fcm/send/abc",
      "https://updates.push.services.mozilla.com/wpush/v2/abc",
      "https://web.push.apple.com/abc",
      "https://wns2-by3p.notify.windows.com/w/?token=abc",
    ]) {
      expect(parseSubscription({ endpoint, keys })).toEqual({ endpoint, ...keys });
    }
  });

  it("refuses any other address", () => {
    for (const endpoint of [
      "http://fcm.googleapis.com/fcm/send/abc",
      "https://fcm.googleapis.com:8443/fcm/send/abc",
      "https://user@fcm.googleapis.com/fcm/send/abc",
      "https://fcm.googleapis.com.example.net/abc",
      "https://evilpush.apple.com/abc",
      "https://push.apple.com.example.net/abc",
      "https://127.0.0.1/abc",
      "https://forge-push.tingouw.com/v1/agents",
      "not an address",
    ]) {
      expect(() => parseSubscription({ endpoint, keys })).toThrow();
    }
    expect(allowedPushServiceHost("notify.windows.com.example.net")).toBe(false);
  });

  it("refuses keys of the wrong form", () => {
    const endpoint = "https://fcm.googleapis.com/fcm/send/abc";
    expect(() => parseSubscription({ endpoint })).toThrow();
    expect(() => parseSubscription({ endpoint, keys: { ...keys, auth: "c2hvcnQ" } })).toThrow();
    expect(() => parseSubscription({ endpoint, keys: { ...keys, p256dh: keys.auth } })).toThrow();
    expect(() => parseSubscription({ endpoint, keys: { ...keys, p256dh: `A${keys.p256dh.slice(1)}` } })).toThrow();
  });

  it("forwards only opaque encrypted content", () => {
    const message = webPushMessage({
      eventId: "opaque-event-id-12345",
      encrypted: { version: 1, keyId: "opaque-key-id-12345", nonce: "AAAAAAAAAAAAAAAA", ciphertext: "opaque-ciphertext-value" },
    });
    expect(message).toEqual({
      version: 1, eventId: "opaque-event-id-12345", keyId: "opaque-key-id-12345",
      nonce: "AAAAAAAAAAAAAAAA", ciphertext: "opaque-ciphertext-value",
    });
    const encoded = JSON.stringify(message);
    expect(encoded).not.toContain("session");
    expect(encoded).not.toContain("title");
  });
});

describe("Requests from a browser", () => {
  it("are allowed from another origin, with the headers that sign them", async () => {
    const preflight = await worker.fetch(
      new Request("https://relay.example/v1/devices/abc/push-endpoint", { method: "OPTIONS" }), {} as Env,
    );
    expect(preflight.status).toBe(204);
    expect(preflight.headers.get("access-control-allow-origin")).toBe("*");
    expect(preflight.headers.get("access-control-allow-methods")).toContain("PUT");
    expect(preflight.headers.get("access-control-allow-headers")).toContain("x-forge-signature");
    const health = await worker.fetch(new Request("https://relay.example/healthz"), {} as Env);
    expect(health.headers.get("access-control-allow-origin")).toBe("*");
  });

  it("learn the key of the relay, or that it has none", async () => {
    const request = new Request("https://relay.example/v1/web-push-key");
    const without = await worker.fetch(request, { VAPID_PUBLIC_KEY: "" } as Env);
    expect(without.status).toBe(503);
    const env = { VAPID_PUBLIC_KEY: "public", VAPID_PRIVATE_KEY: "private", VAPID_SUBJECT: "https://forge.example" } as Env;
    const response = await worker.fetch(request, env);
    expect(await response.json()).toEqual({ publicKey: "public" });
  });
});

import { describe, expect, it } from "vitest";
import {
  base64UrlEncode,
  encryptToken,
  decryptToken,
  requestSigningPayload,
  verifyP256,
} from "../src/crypto";
import { notificationPayload } from "../src/apns";
import { fcmMessage } from "../src/fcm";
import type { PublicJwk } from "../src/types";

describe("Forge push cryptography", () => {
  it("verifies raw P-256 signatures and rejects altered payloads", async () => {
    const pair = await crypto.subtle.generateKey(
      { name: "ECDSA", namedCurve: "P-256" },
      true,
      ["sign", "verify"],
    );
    const exported = await crypto.subtle.exportKey("jwk", pair.publicKey);
    const publicKey: PublicJwk = {
      kty: "EC",
      crv: "P-256",
      x: exported.x!,
      y: exported.y!,
    };
    const payload = "FORGE-REGISTRATION-V1\ndevice\ntest";
    const signature = await crypto.subtle.sign(
      { name: "ECDSA", hash: "SHA-256" },
      pair.privateKey,
      new TextEncoder().encode(payload),
    );
    expect(await verifyP256(publicKey, payload, base64UrlEncode(signature))).toBe(true);
    expect(await verifyP256(publicKey, `${payload}!`, base64UrlEncode(signature))).toBe(false);
  });

  it("binds authenticated requests to method, path, timestamp, nonce, and body", async () => {
    const body = new TextEncoder().encode('{"ok":true}');
    const payload = await requestSigningPayload("post", "/v1/test", "123", "nonce", body);
    expect(payload.split("\n")).toEqual([
      "FORGE-REQUEST-V1",
      "POST",
      "/v1/test",
      "123",
      "nonce",
      "QGLtr3UPuAdOfoPgyQKMlOMkaKi28WFHdDKO8EUVD5M",
    ]);
  });

  it("round trips encrypted push tokens", async () => {
    const secret = base64UrlEncode(crypto.getRandomValues(new Uint8Array(32)));
    const encrypted = await encryptToken("abcdef012345", secret);
    expect(encrypted.ciphertext).not.toContain("abcdef012345");
    expect(await decryptToken(encrypted.ciphertext, encrypted.iv, secret)).toBe("abcdef012345");
  });
});

describe("APNs templates", () => {
  it("forwards only opaque encrypted content", () => {
    const payload = notificationPayload({
      eventId: "opaque-event-id-12345",
      encrypted: { version: 1, keyId: "opaque-key-id-12345", nonce: "AAAAAAAAAAAAAAAA", ciphertext: "opaque-ciphertext-value" },
    });
    expect(payload).toMatchObject({
      aps: { alert: { title: "Forge", body: "Encrypted notification" }, "mutable-content": 1 },
      eventId: "opaque-event-id-12345", keyId: "opaque-key-id-12345",
    });
    const encoded = JSON.stringify(payload);
    expect(encoded).not.toContain("session");
    expect(encoded).not.toContain("completed");
    expect(encoded).not.toContain("approval");
    const background = notificationPayload({
      eventId: "opaque-event-id-12345",
      encrypted: { version: 1, keyId: "opaque-key-id-12345", nonce: "AAAAAAAAAAAAAAAA", ciphertext: "opaque-ciphertext-value" },
    }, true);
    expect(background).toMatchObject({ aps: { "content-available": 1 } });
    expect(JSON.stringify(background)).not.toContain("alert");
  });
});

describe("FCM templates", () => {
  it("forwards only opaque encrypted content", () => {
    const payload = fcmMessage("valid-registration-token-12345", {
      eventId: "opaque-event-id-12345",
      encrypted: { version: 1, keyId: "opaque-key-id-12345", nonce: "AAAAAAAAAAAAAAAA", ciphertext: "opaque-ciphertext-value" },
    });
    expect(payload).toMatchObject({ message: {
      token: "valid-registration-token-12345",
      data: { version: "1", eventId: "opaque-event-id-12345", keyId: "opaque-key-id-12345" },
      android: { priority: "high" },
    } });
    const encoded = JSON.stringify(payload);
    expect(encoded).not.toContain("notification");
    expect(encoded).not.toContain("session");
    expect(encoded).not.toContain("completed");
  });
});

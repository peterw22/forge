import type { PublicJwk } from "./types";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export function arrayBuffer(value: Uint8Array): ArrayBuffer {
  return Uint8Array.from(value).buffer;
}

export function utf8(value: string): Uint8Array {
  return encoder.encode(value);
}

export function decodeUtf8(value: Uint8Array): string {
  return decoder.decode(value);
}

export function base64UrlEncode(value: ArrayBuffer | Uint8Array): string {
  const bytes = value instanceof Uint8Array ? value : new Uint8Array(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/u, "");
}

export function base64UrlDecode(value: string): Uint8Array {
  if (!/^[A-Za-z0-9_-]*$/u.test(value)) throw new Error("invalid base64url");
  const padded = value.replaceAll("-", "+").replaceAll("_", "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
  const binary = atob(padded);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

export function randomId(bytes = 16): string {
  return base64UrlEncode(crypto.getRandomValues(new Uint8Array(bytes)));
}

export async function sha256(value: Uint8Array | string): Promise<Uint8Array> {
  const bytes = typeof value === "string" ? utf8(value) : value;
  return new Uint8Array(await crypto.subtle.digest("SHA-256", arrayBuffer(bytes)));
}

export async function publicKeyFingerprint(jwk: PublicJwk): Promise<string> {
  validatePublicJwk(jwk);
  return base64UrlEncode(await sha256(`${jwk.crv}.${jwk.x}.${jwk.y}`));
}

export function validatePublicJwk(value: unknown): asserts value is PublicJwk {
  if (
    typeof value !== "object" ||
    value === null ||
    (value as Partial<PublicJwk>).kty !== "EC" ||
    (value as Partial<PublicJwk>).crv !== "P-256" ||
    typeof (value as Partial<PublicJwk>).x !== "string" ||
    typeof (value as Partial<PublicJwk>).y !== "string"
  ) {
    throw new Error("publicKey must be an EC P-256 JWK");
  }
  const jwk = value as PublicJwk;
  if (base64UrlDecode(jwk.x).length !== 32 || base64UrlDecode(jwk.y).length !== 32) {
    throw new Error("publicKey coordinates must be 32 bytes");
  }
}

export async function importVerifyKey(jwk: PublicJwk): Promise<CryptoKey> {
  validatePublicJwk(jwk);
  return crypto.subtle.importKey(
    "jwk",
    { ...jwk, ext: true, key_ops: ["verify"] },
    { name: "ECDSA", namedCurve: "P-256" },
    false,
    ["verify"],
  );
}

export async function verifyP256(
  jwk: PublicJwk,
  signingPayload: string,
  signature: string,
): Promise<boolean> {
  const bytes = base64UrlDecode(signature);
  if (bytes.length !== 64) return false;
  const key = await importVerifyKey(jwk);
  return crypto.subtle.verify(
    { name: "ECDSA", hash: "SHA-256" },
    key,
    arrayBuffer(bytes),
    arrayBuffer(utf8(signingPayload)),
  );
}

export async function requestSigningPayload(
  method: string,
  path: string,
  timestamp: string,
  nonce: string,
  body: Uint8Array,
): Promise<string> {
  const bodyHash = base64UrlEncode(await sha256(body));
  return ["FORGE-REQUEST-V1", method.toUpperCase(), path, timestamp, nonce, bodyHash].join("\n");
}

async function importEncryptionKey(secret: string): Promise<CryptoKey> {
  const bytes = base64UrlDecode(secret);
  if (bytes.length !== 32) throw new Error("PUSH_TOKEN_ENCRYPTION_KEY must contain 32 bytes");
  return crypto.subtle.importKey("raw", arrayBuffer(bytes), "AES-GCM", false, ["encrypt", "decrypt"]);
}

export async function encryptToken(
  token: string,
  secret: string,
): Promise<{ ciphertext: string; iv: string; hash: string }> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const key = await importEncryptionKey(secret);
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv: arrayBuffer(iv) }, key, arrayBuffer(utf8(token)));
  return {
    ciphertext: base64UrlEncode(ciphertext),
    iv: base64UrlEncode(iv),
    hash: base64UrlEncode(await sha256(token)),
  };
}

export async function decryptToken(ciphertext: string, iv: string, secret: string): Promise<string> {
  const key = await importEncryptionKey(secret);
  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: arrayBuffer(base64UrlDecode(iv)) },
    key,
    arrayBuffer(base64UrlDecode(ciphertext)),
  );
  return decodeUtf8(new Uint8Array(plaintext));
}

export function timingSafeTextEqual(left: string, right: string): boolean {
  const leftBytes = utf8(left);
  const rightBytes = utf8(right);
  const length = Math.max(leftBytes.length, rightBytes.length);
  let difference = leftBytes.length ^ rightBytes.length;
  for (let index = 0; index < length; index += 1) {
    difference |= (leftBytes[index] ?? 0) ^ (rightBytes[index] ?? 0);
  }
  return difference === 0;
}

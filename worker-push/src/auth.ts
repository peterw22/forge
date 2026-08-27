import { base64UrlDecode, requestSigningPayload, verifyP256 } from "./crypto";
import { ApiError } from "./http";
import type { Env, PrincipalRow, PrincipalType, PublicJwk } from "./types";

export interface AuthenticatedPrincipal {
  type: PrincipalType;
  id: string;
}

export async function authenticateRequest(
  request: Request,
  env: Env,
  body: Uint8Array,
  expectedType: PrincipalType,
  expectedId: string,
): Promise<AuthenticatedPrincipal> {
  const type = request.headers.get("X-Forge-Principal-Type");
  const id = request.headers.get("X-Forge-Principal-ID");
  const timestampText = request.headers.get("X-Forge-Timestamp");
  const nonce = request.headers.get("X-Forge-Nonce");
  const signature = request.headers.get("X-Forge-Signature");
  if (
    type !== expectedType ||
    id !== expectedId ||
    timestampText === null ||
    nonce === null ||
    signature === null
  ) {
    throw new ApiError(401, "authentication_required", "Valid Forge authentication headers are required");
  }
  if (!/^[A-Za-z0-9_-]{16,128}$/u.test(nonce)) {
    throw new ApiError(401, "invalid_nonce", "Request nonce is invalid");
  }
  try {
    if (base64UrlDecode(nonce).length < 16) throw new Error("short nonce");
  } catch {
    throw new ApiError(401, "invalid_nonce", "Request nonce is invalid");
  }
  const timestamp = Number(timestampText);
  const now = Math.floor(Date.now() / 1000);
  const skew = Number(env.REQUEST_CLOCK_SKEW_SECONDS || "300");
  if (!Number.isSafeInteger(timestamp) || Math.abs(now - timestamp) > skew) {
    throw new ApiError(401, "stale_request", "Request timestamp is outside the allowed clock window");
  }

  const table = type === "device" ? "devices" : "agents";
  const principal = await env.DB.prepare(
    `SELECT id, public_key_jwk, revoked_at FROM ${table} WHERE id = ?`,
  )
    .bind(id)
    .first<PrincipalRow>();
  if (principal === null || principal.revoked_at !== null) {
    throw new ApiError(401, "unknown_principal", "Principal is unknown or revoked");
  }

  const payload = await requestSigningPayload(
    request.method,
    new URL(request.url).pathname,
    timestampText,
    nonce,
    body,
  );
  let verified = false;
  try {
    verified = await verifyP256(JSON.parse(principal.public_key_jwk) as PublicJwk, payload, signature);
  } catch {
    verified = false;
  }
  if (!verified) throw new ApiError(401, "invalid_signature", "Request signature is invalid");

  try {
    await env.DB.prepare(
      "INSERT INTO request_nonces (principal_type, principal_id, nonce, expires_at) VALUES (?, ?, ?, ?)",
    )
      .bind(type, id, nonce, now + skew)
      .run();
  } catch (error) {
    if (isD1Constraint(error)) {
      throw new ApiError(409, "replayed_request", "Request nonce has already been used");
    }
    throw error;
  }
  await env.DB.prepare(`UPDATE ${table} SET last_seen_at = ? WHERE id = ?`).bind(now, id).run();
  return { type, id };
}

export function isD1Constraint(error: unknown): boolean {
  return error instanceof Error && /constraint|unique/i.test(error.message);
}

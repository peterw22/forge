import { authenticateRequest, isD1Constraint } from "./auth";
import {
  base64UrlEncode,
  decryptToken,
  encryptToken,
  publicKeyFingerprint,
  randomId,
  sha256,
  timingSafeTextEqual,
  utf8,
  validatePublicJwk,
  verifyP256,
} from "./crypto";
import { sendApns } from "./apns";
import { sendFcm } from "./fcm";
import type { EncryptedPushEnvelope } from "./fcm";
import { ApiError, errorResponse, json, optionalText, readJson, requireText } from "./http";
import type {
  Env,
  PairingRow,
  Platform,
  PublicJwk,
  PushEndpointRow,
  RegistrationChallengeRow,
  Scope,
} from "./types";

const allowedScopes = new Set<Scope>(["notify.approval", "notify.completed"]);

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    try {
      return await route(request, env);
    } catch (error) {
      return errorResponse(error);
    }
  },

  async scheduled(_controller: ScheduledController, env: Env): Promise<void> {
    const now = Math.floor(Date.now() / 1000);
    const eventCutoff = now - 30 * 24 * 60 * 60;
    await env.DB.batch([
      env.DB.prepare("DELETE FROM registration_challenges WHERE expires_at < ?").bind(now),
      env.DB.prepare("DELETE FROM request_nonces WHERE expires_at < ?").bind(now),
      env.DB.prepare("DELETE FROM pairing_challenges WHERE expires_at < ?").bind(now),
      env.DB.prepare("DELETE FROM rate_limits WHERE bucket < ?").bind(Math.floor(now / 60) - 2),
      env.DB.prepare("DELETE FROM push_events WHERE created_at < ?").bind(eventCutoff),
    ]);
  },
} satisfies ExportedHandler<Env>;

async function route(request: Request, env: Env): Promise<Response> {
  const url = new URL(request.url);
  const path = url.pathname;
  if (request.method === "GET" && path === "/healthz") {
    return json({ ok: true, service: "forge-worker-push" });
  }
  if (request.method === "POST" && path === "/v1/registration-challenges") {
    return createRegistrationChallenge(request, env);
  }
  if (request.method === "POST" && path === "/v1/registrations") {
    return completeRegistration(request, env);
  }

  let match = /^\/v1\/devices\/([^/]+)\/push-endpoint$/u.exec(path);
  if (match?.[1] !== undefined && request.method === "PUT") {
    return putPushEndpoint(request, env, decodeURIComponent(match[1]));
  }
  if (match?.[1] !== undefined && request.method === "DELETE") {
    return deletePushEndpoint(request, env, decodeURIComponent(match[1]));
  }

  match = /^\/v1\/agents\/([^/]+)\/authorizations$/u.exec(path);
  if (match?.[1] !== undefined && request.method === "GET") {
    return listAgentAuthorizations(request, env, decodeURIComponent(match[1]));
  }
  match = /^\/v1\/agents\/([^/]+)\/authorizations\/([^/]+)$/u.exec(path);
  if (match?.[1] !== undefined && match[2] !== undefined && request.method === "DELETE") {
    return revokeAgentAuthorization(
      request,
      env,
      decodeURIComponent(match[1]),
      decodeURIComponent(match[2]),
    );
  }

  match = /^\/v1\/agents\/([^/]+)\/pairings$/u.exec(path);
  if (match?.[1] !== undefined && request.method === "POST") {
    return createPairing(request, env, decodeURIComponent(match[1]));
  }
  match = /^\/v1\/agents\/([^/]+)\/pairings\/([^/]+)$/u.exec(path);
  if (match?.[1] !== undefined && match[2] !== undefined && request.method === "GET") {
    return getAgentPairing(request, env, decodeURIComponent(match[1]), decodeURIComponent(match[2]));
  }
  match = /^\/v1\/devices\/([^/]+)\/pairings\/([^/]+)$/u.exec(path);
  if (match?.[1] !== undefined && match[2] !== undefined && request.method === "GET") {
    return getDevicePairing(request, env, decodeURIComponent(match[1]), decodeURIComponent(match[2]));
  }
  match = /^\/v1\/devices\/([^/]+)\/pairings\/([^/]+)\/approve$/u.exec(path);
  if (match?.[1] !== undefined && match[2] !== undefined && request.method === "POST") {
    return approvePairing(request, env, decodeURIComponent(match[1]), decodeURIComponent(match[2]));
  }

  match = /^\/v1\/devices\/([^/]+)\/authorizations$/u.exec(path);
  if (match?.[1] !== undefined && request.method === "GET") {
    return listAuthorizations(request, env, decodeURIComponent(match[1]));
  }
  match = /^\/v1\/devices\/([^/]+)\/authorizations\/([^/]+)$/u.exec(path);
  if (match?.[1] !== undefined && match[2] !== undefined && request.method === "DELETE") {
    return revokeAuthorization(request, env, decodeURIComponent(match[1]), decodeURIComponent(match[2]));
  }

  match = /^\/v1\/agents\/([^/]+)\/events$/u.exec(path);
  if (match?.[1] !== undefined && request.method === "POST") {
    return createPushEvent(request, env, decodeURIComponent(match[1]));
  }
  throw new ApiError(404, "not_found", "Endpoint not found");
}

interface RegistrationChallengeBody {
  principalType?: unknown;
  principalId?: unknown;
  platform?: unknown;
  publicKey?: unknown;
  displayName?: unknown;
}

async function createRegistrationChallenge(request: Request, env: Env): Promise<Response> {
  const { body } = await readJson<RegistrationChallengeBody>(request);
  const principalType = body.principalType;
  if (principalType !== "device" && principalType !== "agent") {
    throw new ApiError(400, "invalid_request", "principalType must be device or agent");
  }
  const principalId = requireIdentifier(body.principalId, "principalId");
  let platform: Platform | null = null;
  if (principalType === "device") {
    if (body.platform !== "ios" && body.platform !== "android" && body.platform !== "macos") {
      throw new ApiError(400, "invalid_request", "platform must be ios, android, or macos");
    }
    platform = body.platform;
  }
  try {
    validatePublicJwk(body.publicKey);
  } catch (error) {
    throw new ApiError(400, "invalid_public_key", error instanceof Error ? error.message : "Invalid public key");
  }
  const publicKey = body.publicKey;
  const fingerprint = await publicKeyFingerprint(publicKey);
  const table = principalType === "device" ? "devices" : "agents";
  const existing = await env.DB.prepare(`SELECT fingerprint FROM ${table} WHERE id = ?`)
    .bind(principalId)
    .first<{ fingerprint: string }>();
  if (existing !== null && existing.fingerprint !== fingerprint) {
    throw new ApiError(409, "identity_exists", "This identity ID is already registered with another key");
  }
  const id = randomId();
  const now = Math.floor(Date.now() / 1000);
  const expiresAt = now + numberSetting(env.REGISTRATION_TTL_SECONDS, 300);
  const challenge = randomId(32);
  const signingPayload = [
    "FORGE-REGISTRATION-V1",
    principalType,
    principalId,
    id,
    challenge,
    fingerprint,
    String(expiresAt),
  ].join("\n");
  await env.DB.prepare(
    `INSERT INTO registration_challenges
      (id, principal_type, principal_id, platform, public_key_jwk, fingerprint, display_name, signing_payload, expires_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
  )
    .bind(
      id,
      principalType,
      principalId,
      platform,
      JSON.stringify(publicKey),
      fingerprint,
      optionalText(body.displayName, "displayName"),
      signingPayload,
      expiresAt,
    )
    .run();
  return json({ challengeId: id, signingPayload, fingerprint, expiresAt }, 201);
}

interface CompleteRegistrationBody {
  challengeId?: unknown;
  signature?: unknown;
}

async function completeRegistration(request: Request, env: Env): Promise<Response> {
  const { body } = await readJson<CompleteRegistrationBody>(request);
  const challengeId = requireIdentifier(body.challengeId, "challengeId");
  const signature = requireText(body.signature, "signature", 256);
  const challenge = await env.DB.prepare("SELECT * FROM registration_challenges WHERE id = ?")
    .bind(challengeId)
    .first<RegistrationChallengeRow>();
  const now = Math.floor(Date.now() / 1000);
  if (challenge === null || challenge.consumed_at !== null || challenge.expires_at < now) {
    throw new ApiError(410, "challenge_expired", "Registration challenge is unavailable or expired");
  }
  const publicKey = JSON.parse(challenge.public_key_jwk) as PublicJwk;
  if (!(await verifyP256(publicKey, challenge.signing_payload, signature))) {
    throw new ApiError(401, "invalid_signature", "Registration proof is invalid");
  }
  const table = challenge.principal_type === "device" ? "devices" : "agents";
  const registered = await env.DB.prepare(`SELECT fingerprint FROM ${table} WHERE id = ?`)
    .bind(challenge.principal_id)
    .first<{ fingerprint: string }>();
  if (registered !== null && registered.fingerprint !== challenge.fingerprint) {
    throw new ApiError(409, "identity_exists", "This identity ID is already registered with another key");
  }
  if (table === "devices") {
    await env.DB.batch([
      env.DB.prepare(
        `INSERT INTO devices (id, platform, public_key_jwk, fingerprint, display_name, created_at, last_seen_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(id) DO UPDATE SET last_seen_at = excluded.last_seen_at
         WHERE devices.fingerprint = excluded.fingerprint`,
      ).bind(
        challenge.principal_id,
        challenge.platform,
        challenge.public_key_jwk,
        challenge.fingerprint,
        challenge.display_name,
        now,
        now,
      ),
      env.DB.prepare("UPDATE registration_challenges SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL")
        .bind(now, challenge.id),
    ]);
  } else {
    await env.DB.batch([
      env.DB.prepare(
        `INSERT INTO agents (id, public_key_jwk, fingerprint, display_name, created_at, last_seen_at)
         VALUES (?, ?, ?, ?, ?, ?)
         ON CONFLICT(id) DO UPDATE SET last_seen_at = excluded.last_seen_at
         WHERE agents.fingerprint = excluded.fingerprint`,
      ).bind(
        challenge.principal_id,
        challenge.public_key_jwk,
        challenge.fingerprint,
        challenge.display_name,
        now,
        now,
      ),
      env.DB.prepare("UPDATE registration_challenges SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL")
        .bind(now, challenge.id),
    ]);
  }
  return json({ registered: true, principalType: challenge.principal_type, principalId: challenge.principal_id });
}

interface PushEndpointBody {
  provider?: unknown;
  environment?: unknown;
  topic?: unknown;
  token?: unknown;
}

async function putPushEndpoint(request: Request, env: Env, deviceId: string): Promise<Response> {
  const { body, bytes } = await readJson<PushEndpointBody>(request);
  await authenticateRequest(request, env, bytes, "device", deviceId);
  if (body.provider !== "apns" && body.provider !== "fcm") {
    throw new ApiError(400, "unsupported_provider", "provider must be apns or fcm");
  }
  const device = await env.DB.prepare("SELECT platform FROM devices WHERE id = ? AND revoked_at IS NULL")
    .bind(deviceId)
    .first<{ platform: Platform }>();
  if (device === null) throw new ApiError(404, "device_not_found", "Device is not registered");
  const expectedProvider = device.platform === "android" ? "fcm" : "apns";
  if (body.provider !== expectedProvider) {
    throw new ApiError(400, "provider_platform_mismatch", `Provider ${body.provider} is not valid for ${device.platform}`);
  }
  if (body.environment !== "development" && body.environment !== "production") {
    throw new ApiError(400, "invalid_request", "environment must be development or production");
  }
  if (body.provider === "fcm" && body.environment !== "production") {
    throw new ApiError(400, "invalid_request", "FCM endpoints must use production environment");
  }
  const topic = requireText(body.topic, "topic", 200);
  if (topic !== env.APNS_TOPIC) throw new ApiError(400, "invalid_topic", "Push topic is not allowed");
  const token = requireText(body.token, "token", 4096);
  if (body.provider === "apns" && !/^[0-9a-fA-F]{32,256}$/u.test(token)) {
    throw new ApiError(400, "invalid_push_token", "APNs device token is invalid");
  }
  if (body.provider === "fcm" && (token.length < 20 || /\s/u.test(token))) {
    throw new ApiError(400, "invalid_push_token", "FCM registration token is invalid");
  }
  const normalizedToken = body.provider === "apns" ? token.toLowerCase() : token;
  const protectedToken = await encryptToken(normalizedToken, env.PUSH_TOKEN_ENCRYPTION_KEY);
  const now = Math.floor(Date.now() / 1000);
  const id = randomId();
  await env.DB.prepare(
    `INSERT INTO push_endpoints
      (id, device_id, provider, environment, topic, token_ciphertext, token_iv, token_hash, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
     ON CONFLICT(device_id, provider, environment, topic) DO UPDATE SET
       token_ciphertext = excluded.token_ciphertext,
       token_iv = excluded.token_iv,
       token_hash = excluded.token_hash,
       updated_at = excluded.updated_at,
       disabled_at = NULL,
       disabled_reason = NULL`,
  )
    .bind(id, deviceId, body.provider, body.environment, topic, protectedToken.ciphertext,
      protectedToken.iv, protectedToken.hash, now, now)
    .run();
  return json({ registered: true, provider: body.provider, environment: body.environment, topic });
}

async function deletePushEndpoint(request: Request, env: Env, deviceId: string): Promise<Response> {
  const bytes = new Uint8Array(await request.arrayBuffer());
  await authenticateRequest(request, env, bytes, "device", deviceId);
  const now = Math.floor(Date.now() / 1000);
  await env.DB.prepare(
    "UPDATE push_endpoints SET disabled_at = ?, disabled_reason = 'device_unregistered' WHERE device_id = ? AND disabled_at IS NULL",
  )
    .bind(now, deviceId)
    .run();
  return json({ disabled: true });
}

interface PairingBody {
  deviceId?: unknown;
  scopes?: unknown;
}

async function createPairing(request: Request, env: Env, agentId: string): Promise<Response> {
  const { body, bytes } = await readJson<PairingBody>(request);
  await authenticateRequest(request, env, bytes, "agent", agentId);
  const deviceId = requireIdentifier(body.deviceId, "deviceId");
  const scopes = parseScopes(body.scopes);
  const device = await env.DB.prepare("SELECT id FROM devices WHERE id = ? AND revoked_at IS NULL")
    .bind(deviceId)
    .first();
  if (device === null) throw new ApiError(404, "device_not_found", "Target device is not registered");
  const id = randomId();
  const verificationCode = String(crypto.getRandomValues(new Uint32Array(1))[0]! % 1_000_000).padStart(6, "0");
  const codeHash = base64UrlEncode(await sha256(`${id}:${verificationCode}`));
  const now = Math.floor(Date.now() / 1000);
  const expiresAt = now + numberSetting(env.PAIRING_TTL_SECONDS, 300);
  const signingPayload = [
    "FORGE-PAIRING-V1",
    id,
    deviceId,
    agentId,
    scopes.join(","),
    codeHash,
    String(expiresAt),
  ].join("\n");
  await env.DB.prepare(
    `INSERT INTO pairing_challenges
      (id, device_id, agent_id, requested_scopes, verification_code_hash, signing_payload, created_at, expires_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
  )
    .bind(id, deviceId, agentId, JSON.stringify(scopes), codeHash, signingPayload, now, expiresAt)
    .run();
  return json({ pairingId: id, verificationCode, scopes, expiresAt }, 201);
}

async function getAgentPairing(request: Request, env: Env, agentId: string, pairingId: string): Promise<Response> {
  const bytes = new Uint8Array(await request.arrayBuffer());
  await authenticateRequest(request, env, bytes, "agent", agentId);
  const pairing = await loadPairing(env, pairingId);
  if (pairing.agent_id !== agentId) throw new ApiError(403, "forbidden", "Pairing does not belong to this agent");
  return json(pairingStatus(pairing));
}

async function getDevicePairing(request: Request, env: Env, deviceId: string, pairingId: string): Promise<Response> {
  const bytes = new Uint8Array(await request.arrayBuffer());
  await authenticateRequest(request, env, bytes, "device", deviceId);
  const pairing = await loadPairing(env, pairingId);
  if (pairing.device_id !== deviceId) throw new ApiError(403, "forbidden", "Pairing does not belong to this device");
  const agent = await env.DB.prepare("SELECT display_name, fingerprint FROM agents WHERE id = ?")
    .bind(pairing.agent_id)
    .first<{ display_name: string | null; fingerprint: string }>();
  return json({
    ...pairingStatus(pairing),
    signingPayload: pairing.signing_payload,
    agent: { id: pairing.agent_id, displayName: agent?.display_name ?? null, fingerprint: agent?.fingerprint ?? "" },
  });
}

interface ApprovePairingBody {
  signature?: unknown;
  verificationCode?: unknown;
}

async function approvePairing(
  request: Request,
  env: Env,
  deviceId: string,
  pairingId: string,
): Promise<Response> {
  const { body, bytes } = await readJson<ApprovePairingBody>(request);
  await authenticateRequest(request, env, bytes, "device", deviceId);
  const pairing = await loadPairing(env, pairingId);
  const now = Math.floor(Date.now() / 1000);
  if (pairing.device_id !== deviceId) throw new ApiError(403, "forbidden", "Pairing does not belong to this device");
  if (pairing.expires_at < now || pairing.consumed_at !== null || pairing.denied_at !== null) {
    throw new ApiError(410, "pairing_expired", "Pairing is unavailable or expired");
  }
  const verificationCode = requireText(body.verificationCode, "verificationCode", 6);
  const expectedCodeHash = await env.DB.prepare("SELECT verification_code_hash FROM pairing_challenges WHERE id = ?")
    .bind(pairingId)
    .first<{ verification_code_hash: string }>();
  const actualCodeHash = base64UrlEncode(await sha256(`${pairingId}:${verificationCode}`));
  if (expectedCodeHash === null || !timingSafeTextEqual(expectedCodeHash.verification_code_hash, actualCodeHash)) {
    throw new ApiError(403, "verification_code_mismatch", "Pairing verification code does not match");
  }
  const signature = requireText(body.signature, "signature", 256);
  const device = await env.DB.prepare("SELECT public_key_jwk FROM devices WHERE id = ? AND revoked_at IS NULL")
    .bind(deviceId)
    .first<{ public_key_jwk: string }>();
  if (
    device === null ||
    !(await verifyP256(JSON.parse(device.public_key_jwk) as PublicJwk, pairing.signing_payload, signature))
  ) {
    throw new ApiError(401, "invalid_signature", "Pairing approval signature is invalid");
  }
  const scopes = parseStoredScopes(pairing.requested_scopes);
  await env.DB.batch([
    env.DB.prepare(
      `INSERT INTO authorizations (id, device_id, agent_id, scopes, created_at, updated_at)
       VALUES (?, ?, ?, ?, ?, ?)
       ON CONFLICT(device_id, agent_id) DO UPDATE SET
         scopes = excluded.scopes,
         updated_at = excluded.updated_at,
         revoked_at = NULL`,
    ).bind(randomId(), deviceId, pairing.agent_id, JSON.stringify(scopes), now, now),
    env.DB.prepare("UPDATE pairing_challenges SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL")
      .bind(now, pairingId),
  ]);
  return json({ approved: true, agentId: pairing.agent_id, deviceId, scopes });
}

async function listAgentAuthorizations(request: Request, env: Env, agentId: string): Promise<Response> {
  const bytes = new Uint8Array(await request.arrayBuffer());
  await authenticateRequest(request, env, bytes, "agent", agentId);
  const rows = await env.DB.prepare(
    `SELECT authorizations.device_id, authorizations.scopes, authorizations.created_at,
            authorizations.updated_at, devices.display_name, devices.fingerprint, devices.platform,
            EXISTS(
              SELECT 1 FROM push_endpoints
               WHERE push_endpoints.device_id = devices.id
                 AND push_endpoints.disabled_at IS NULL
            ) AS endpoint_active
       FROM authorizations
       JOIN devices ON devices.id = authorizations.device_id
      WHERE authorizations.agent_id = ?
        AND authorizations.revoked_at IS NULL
        AND devices.revoked_at IS NULL
      ORDER BY authorizations.updated_at DESC`,
  )
    .bind(agentId)
    .all<{
      device_id: string;
      scopes: string;
      created_at: number;
      updated_at: number;
      display_name: string | null;
      fingerprint: string;
      platform: Platform;
      endpoint_active: number;
    }>();
  return json({
    authorizations: rows.results.map((row) => ({
      deviceId: row.device_id,
      scopes: parseStoredScopes(row.scopes),
      createdAt: row.created_at,
      updatedAt: row.updated_at,
      displayName: row.display_name,
      fingerprint: row.fingerprint,
      platform: row.platform,
      pushEndpointActive: row.endpoint_active === 1,
    })),
  });
}

async function revokeAgentAuthorization(
  request: Request,
  env: Env,
  agentId: string,
  deviceId: string,
): Promise<Response> {
  const bytes = new Uint8Array(await request.arrayBuffer());
  await authenticateRequest(request, env, bytes, "agent", agentId);
  const now = Math.floor(Date.now() / 1000);
  await env.DB.prepare(
    "UPDATE authorizations SET revoked_at = ?, updated_at = ? WHERE device_id = ? AND agent_id = ? AND revoked_at IS NULL",
  )
    .bind(now, now, deviceId, agentId)
    .run();
  return json({ revoked: true, deviceId });
}

async function listAuthorizations(request: Request, env: Env, deviceId: string): Promise<Response> {
  const bytes = new Uint8Array(await request.arrayBuffer());
  await authenticateRequest(request, env, bytes, "device", deviceId);
  const rows = await env.DB.prepare(
    `SELECT authorizations.agent_id, authorizations.scopes, authorizations.created_at,
            agents.display_name, agents.fingerprint
       FROM authorizations
       JOIN agents ON agents.id = authorizations.agent_id
      WHERE authorizations.device_id = ? AND authorizations.revoked_at IS NULL`,
  )
    .bind(deviceId)
    .all<{ agent_id: string; scopes: string; created_at: number; display_name: string | null; fingerprint: string }>();
  return json({
    authorizations: rows.results.map((row) => ({
      agentId: row.agent_id,
      scopes: parseStoredScopes(row.scopes),
      createdAt: row.created_at,
      displayName: row.display_name,
      fingerprint: row.fingerprint,
    })),
  });
}

async function revokeAuthorization(
  request: Request,
  env: Env,
  deviceId: string,
  agentId: string,
): Promise<Response> {
  const bytes = new Uint8Array(await request.arrayBuffer());
  await authenticateRequest(request, env, bytes, "device", deviceId);
  await env.DB.prepare(
    "UPDATE authorizations SET revoked_at = ?, updated_at = ? WHERE device_id = ? AND agent_id = ? AND revoked_at IS NULL",
  )
    .bind(Math.floor(Date.now() / 1000), Math.floor(Date.now() / 1000), deviceId, agentId)
    .run();
  return json({ revoked: true, agentId });
}

interface EventBody {
  deviceId?: unknown;
  eventId?: unknown;
  type?: unknown;
  sessionId?: unknown;
  encrypted?: unknown;
}

async function createPushEvent(request: Request, env: Env, agentId: string): Promise<Response> {
  const { body, bytes } = await readJson<EventBody>(request);
  await authenticateRequest(request, env, bytes, "agent", agentId);
  const deviceId = requireIdentifier(body.deviceId, "deviceId");
  const eventId = requireIdentifier(body.eventId, "eventId");
  const device = await env.DB.prepare("SELECT platform FROM devices WHERE id = ? AND revoked_at IS NULL")
    .bind(deviceId).first<{ platform: Platform }>();
  if (device === null) throw new ApiError(404, "device_not_found", "Device is not registered");
  const eventType = "encrypted";
  const sessionId = "encrypted";
  const encrypted = parseEncryptedEnvelope(body.encrypted);
  await enforceEventRateLimit(env, agentId);
  const authorization = await env.DB.prepare(
    "SELECT scopes FROM authorizations WHERE device_id = ? AND agent_id = ? AND revoked_at IS NULL",
  )
    .bind(deviceId, agentId)
    .first<{ scopes: string }>();
  const scopes = authorization === null ? [] : parseStoredScopes(authorization.scopes);
  if (authorization === null ||
      !scopes.includes("notify.approval") || !scopes.includes("notify.completed")) {
    throw new ApiError(403, "not_authorized", "Agent is not authorized for this device");
  }
  const now = Math.floor(Date.now() / 1000);
  try {
    await env.DB.prepare(
      `INSERT INTO push_events
        (agent_id, event_id, device_id, event_type, session_id, created_at, delivery_status)
       VALUES (?, ?, ?, ?, ?, ?, 'sending')`,
    )
      .bind(agentId, eventId, deviceId, eventType, sessionId, now)
      .run();
  } catch (error) {
    if (isD1Constraint(error)) return json({ accepted: true, duplicate: true, eventId });
    throw error;
  }

  const provider = device.platform === "android" ? "fcm" : "apns";
  const endpoint = await env.DB.prepare(
    `SELECT id, device_id, provider, environment, topic, token_ciphertext, token_iv, disabled_at
       FROM push_endpoints
      WHERE device_id = ? AND provider = ? AND disabled_at IS NULL
      ORDER BY updated_at DESC LIMIT 1`,
  )
    .bind(deviceId, provider)
    .first<PushEndpointRow>();
  if (endpoint === null) {
    await recordDelivery(env, agentId, eventId, "no_endpoint", null, "NoActiveEndpoint", null);
    throw new ApiError(409, "no_push_endpoint", `Device has no active ${provider.toUpperCase()} endpoint`);
  }
  const token = await decryptToken(endpoint.token_ciphertext, endpoint.token_iv, env.PUSH_TOKEN_ENCRYPTION_KEY);
  const opaqueEvent = { eventId, encrypted };
  const result = provider === "apns"
    ? await sendApns(
        env,
        { token, environment: endpoint.environment, topic: endpoint.topic },
        opaqueEvent,
        device.platform === "macos",
      )
    : await sendFcm(env, token, opaqueEvent);
  const delivered = result.status >= 200 && result.status < 300;
  await recordDelivery(env, agentId, eventId, delivered ? "delivered" : "failed",
    result.status, result.reason, delivered ? now : null);
  const invalidToken = provider === "apns"
    ? result.status === 410 || ["BadDeviceToken", "DeviceTokenNotForTopic", "Unregistered"].includes(result.reason ?? "")
    : ["UNREGISTERED", "INVALID_ARGUMENT", "SENDER_ID_MISMATCH"].includes(result.reason ?? "");
  if (invalidToken) {
    await env.DB.prepare("UPDATE push_endpoints SET disabled_at = ?, disabled_reason = ? WHERE id = ?")
      .bind(now, result.reason ?? `${provider} ${result.status}`, endpoint.id)
      .run();
  }
  if (!delivered) {
    throw new ApiError(result.status >= 500 || result.status === 429 ? 503 : 502,
      `${provider}_rejected`, `${provider.toUpperCase()} rejected the notification: ${result.reason ?? result.status}`);
  }
  const providerMessageId = "apnsId" in result ? result.apnsId : result.messageId;
  return json({ accepted: true, delivered: true, eventId, provider, providerMessageId }, 202);
}

function parseEncryptedEnvelope(value: unknown): EncryptedPushEnvelope {
  if (typeof value !== "object" || value === null) {
    throw new ApiError(400, "encrypted_push_required", "Android push content must be encrypted");
  }
  const body = value as Record<string, unknown>;
  if (body.version !== 1) throw new ApiError(400, "invalid_envelope", "Unsupported push envelope version");
  const keyId = requireIdentifier(body.keyId, "keyId");
  const nonce = requireText(body.nonce, "nonce", 32);
  const ciphertext = requireText(body.ciphertext, "ciphertext", 12000);
  if (!/^[A-Za-z0-9_-]{16}$/u.test(nonce) || !/^[A-Za-z0-9_-]{24,12000}$/u.test(ciphertext)) {
    throw new ApiError(400, "invalid_envelope", "Encrypted push encoding is invalid");
  }
  return { version: 1, keyId, nonce, ciphertext };
}

async function loadPairing(env: Env, pairingId: string): Promise<PairingRow> {
  const pairing = await env.DB.prepare("SELECT * FROM pairing_challenges WHERE id = ?")
    .bind(pairingId)
    .first<PairingRow>();
  if (pairing === null) throw new ApiError(404, "pairing_not_found", "Pairing was not found");
  return pairing;
}

function pairingStatus(pairing: PairingRow): Record<string, unknown> {
  const now = Math.floor(Date.now() / 1000);
  const status = pairing.consumed_at !== null
    ? "approved"
    : pairing.denied_at !== null
      ? "denied"
      : pairing.expires_at < now
        ? "expired"
        : "pending";
  return {
    pairingId: pairing.id,
    deviceId: pairing.device_id,
    agentId: pairing.agent_id,
    scopes: parseStoredScopes(pairing.requested_scopes),
    status,
    expiresAt: pairing.expires_at,
  };
}

function parseScopes(value: unknown): Scope[] {
  if (!Array.isArray(value) || value.length === 0 || value.length > allowedScopes.size) {
    throw new ApiError(400, "invalid_scopes", "At least one supported scope is required");
  }
  const scopes = [...new Set(value)];
  if (scopes.some((scope) => typeof scope !== "string" || !allowedScopes.has(scope as Scope))) {
    throw new ApiError(400, "invalid_scopes", "One or more requested scopes are unsupported");
  }
  return (scopes as Scope[]).sort();
}

function parseStoredScopes(value: string): Scope[] {
  return parseScopes(JSON.parse(value) as unknown);
}

function requireIdentifier(value: unknown, field: string): string {
  const identifier = requireText(value, field, 128);
  if (!/^[A-Za-z0-9_-]{16,128}$/u.test(identifier)) {
    throw new ApiError(400, "invalid_request", `${field} must be a base64url identifier`);
  }
  return identifier;
}

function numberSetting(value: string, fallback: number): number {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : fallback;
}

async function enforceEventRateLimit(env: Env, agentId: string): Promise<void> {
  const now = Math.floor(Date.now() / 1000);
  const bucket = Math.floor(now / 60);
  await env.DB.prepare(
    `INSERT INTO rate_limits (principal_type, principal_id, bucket, request_count)
     VALUES ('agent', ?, ?, 1)
     ON CONFLICT(principal_type, principal_id, bucket)
     DO UPDATE SET request_count = request_count + 1`,
  )
    .bind(agentId, bucket)
    .run();
  const row = await env.DB.prepare(
    "SELECT request_count FROM rate_limits WHERE principal_type = 'agent' AND principal_id = ? AND bucket = ?",
  )
    .bind(agentId, bucket)
    .first<{ request_count: number }>();
  if ((row?.request_count ?? 0) > numberSetting(env.EVENT_RATE_LIMIT_PER_MINUTE, 30)) {
    throw new ApiError(429, "rate_limited", "Agent event rate limit exceeded");
  }
}

async function recordDelivery(
  env: Env,
  agentId: string,
  eventId: string,
  status: string,
  providerStatus: number | null,
  providerReason: string | null,
  deliveredAt: number | null,
): Promise<void> {
  await env.DB.prepare(
    `UPDATE push_events
        SET delivery_status = ?, provider_status = ?, provider_reason = ?, delivered_at = ?
      WHERE agent_id = ? AND event_id = ?`,
  )
    .bind(status, providerStatus, providerReason, deliveredAt, agentId, eventId)
    .run();
}

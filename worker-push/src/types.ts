export interface Env {
  DB: D1Database;
  APNS_PRIVATE_KEY_P8: string;
  APNS_KEY_ID: string;
  APNS_TEAM_ID: string;
  PUSH_TOKEN_ENCRYPTION_KEY: string;
  APNS_TOPIC: string;
  FCM_PROJECT_ID: string;
  FCM_SERVICE_ACCOUNT_JSON: string;
  VAPID_PUBLIC_KEY: string;
  VAPID_PRIVATE_KEY: string;
  VAPID_SUBJECT: string;
  REGISTRATION_TTL_SECONDS: string;
  PAIRING_TTL_SECONDS: string;
  REQUEST_CLOCK_SKEW_SECONDS: string;
  EVENT_RATE_LIMIT_PER_MINUTE: string;
}

export type PrincipalType = "device" | "agent";
export type Platform = "ios" | "android" | "macos" | "web";
export type Provider = "apns" | "fcm" | "webpush";
export type Scope = "notify.approval" | "notify.completed";
export type EventType = "approval_required" | "session_completed";

export interface PublicJwk {
  kty: "EC";
  crv: "P-256";
  x: string;
  y: string;
}

export interface PrincipalRow {
  id: string;
  public_key_jwk: string;
  revoked_at: number | null;
}

export interface RegistrationChallengeRow {
  id: string;
  principal_type: PrincipalType;
  principal_id: string;
  platform: Platform | null;
  public_key_jwk: string;
  fingerprint: string;
  display_name: string | null;
  signing_payload: string;
  expires_at: number;
  consumed_at: number | null;
}

export interface PairingRow {
  id: string;
  device_id: string;
  agent_id: string;
  requested_scopes: string;
  signing_payload: string;
  expires_at: number;
  consumed_at: number | null;
  denied_at: number | null;
}

export interface PushEndpointRow {
  id: string;
  device_id: string;
  provider: Provider;
  environment: "development" | "production";
  topic: string;
  token_ciphertext: string;
  token_iv: string;
  disabled_at: number | null;
}

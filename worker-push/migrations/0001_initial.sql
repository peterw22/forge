-- Forge push relay initial schema.
PRAGMA foreign_keys = ON;

CREATE TABLE devices (
  id TEXT PRIMARY KEY,
  platform TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
  public_key_jwk TEXT NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  display_name TEXT,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  revoked_at INTEGER
);

CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  public_key_jwk TEXT NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  display_name TEXT,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  revoked_at INTEGER
);

CREATE TABLE registration_challenges (
  id TEXT PRIMARY KEY,
  principal_type TEXT NOT NULL CHECK (principal_type IN ('device', 'agent')),
  principal_id TEXT NOT NULL,
  platform TEXT,
  public_key_jwk TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  display_name TEXT,
  signing_payload TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  consumed_at INTEGER
);
CREATE INDEX registration_challenges_expiry_idx ON registration_challenges(expires_at);

CREATE TABLE push_endpoints (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  provider TEXT NOT NULL CHECK (provider IN ('apns', 'fcm')),
  environment TEXT NOT NULL CHECK (environment IN ('development', 'production')),
  topic TEXT NOT NULL,
  token_ciphertext TEXT NOT NULL,
  token_iv TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  disabled_at INTEGER,
  disabled_reason TEXT,
  FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
  UNIQUE (device_id, provider, environment, topic)
);
CREATE INDEX push_endpoints_device_idx ON push_endpoints(device_id, disabled_at);

CREATE TABLE pairing_challenges (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  requested_scopes TEXT NOT NULL,
  verification_code_hash TEXT NOT NULL,
  signing_payload TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  consumed_at INTEGER,
  denied_at INTEGER,
  FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
);
CREATE INDEX pairing_device_idx ON pairing_challenges(device_id, expires_at);
CREATE INDEX pairing_agent_idx ON pairing_challenges(agent_id, expires_at);

CREATE TABLE authorizations (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  scopes TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  revoked_at INTEGER,
  FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
  UNIQUE (device_id, agent_id)
);
CREATE INDEX authorizations_agent_idx ON authorizations(agent_id, revoked_at);

CREATE TABLE request_nonces (
  principal_type TEXT NOT NULL CHECK (principal_type IN ('device', 'agent')),
  principal_id TEXT NOT NULL,
  nonce TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (principal_type, principal_id, nonce)
);
CREATE INDEX request_nonces_expiry_idx ON request_nonces(expires_at);

CREATE TABLE push_events (
  agent_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN ('approval_required', 'session_completed')),
  session_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  delivery_status TEXT NOT NULL,
  provider_status INTEGER,
  provider_reason TEXT,
  delivered_at INTEGER,
  FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
  FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
  PRIMARY KEY (agent_id, event_id)
);
CREATE INDEX push_events_created_idx ON push_events(created_at);

CREATE TABLE rate_limits (
  principal_type TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  bucket INTEGER NOT NULL,
  request_count INTEGER NOT NULL,
  PRIMARY KEY (principal_type, principal_id, bucket)
);

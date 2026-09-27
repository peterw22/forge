-- Add browsers as devices, and Web Push as the provider that reaches them.
--
-- Dropping devices deletes the rows that refer to it where foreign keys stay
-- enforced, so those tables are copied first and restored afterwards.
PRAGMA foreign_keys = OFF;

CREATE TABLE push_endpoints_copy AS SELECT * FROM push_endpoints;
CREATE TABLE pairing_challenges_copy AS SELECT * FROM pairing_challenges;
CREATE TABLE authorizations_copy AS SELECT * FROM authorizations;
CREATE TABLE push_events_copy AS SELECT * FROM push_events;

CREATE TABLE devices_v3 (
  id TEXT PRIMARY KEY,
  platform TEXT NOT NULL CHECK (platform IN ('ios', 'android', 'macos', 'web')),
  public_key_jwk TEXT NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  display_name TEXT,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  revoked_at INTEGER
);

INSERT INTO devices_v3
  (id, platform, public_key_jwk, fingerprint, display_name, created_at, last_seen_at, revoked_at)
SELECT id, platform, public_key_jwk, fingerprint, display_name, created_at, last_seen_at, revoked_at
FROM devices;

DROP TABLE devices;
ALTER TABLE devices_v3 RENAME TO devices;

CREATE TABLE push_endpoints_v2 (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  provider TEXT NOT NULL CHECK (provider IN ('apns', 'fcm', 'webpush')),
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

INSERT INTO push_endpoints_v2
  (id, device_id, provider, environment, topic, token_ciphertext, token_iv, token_hash,
   created_at, updated_at, disabled_at, disabled_reason)
SELECT id, device_id, provider, environment, topic, token_ciphertext, token_iv, token_hash,
   created_at, updated_at, disabled_at, disabled_reason
FROM push_endpoints_copy;

DROP TABLE push_endpoints;
ALTER TABLE push_endpoints_v2 RENAME TO push_endpoints;
CREATE INDEX push_endpoints_device_idx ON push_endpoints(device_id, disabled_at);

INSERT OR IGNORE INTO pairing_challenges SELECT * FROM pairing_challenges_copy;
INSERT OR IGNORE INTO authorizations SELECT * FROM authorizations_copy;
INSERT OR IGNORE INTO push_events SELECT * FROM push_events_copy;

DROP TABLE push_endpoints_copy;
DROP TABLE pairing_challenges_copy;
DROP TABLE authorizations_copy;
DROP TABLE push_events_copy;

PRAGMA foreign_keys = ON;

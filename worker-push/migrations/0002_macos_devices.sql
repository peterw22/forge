-- Add native macOS devices while preserving existing device identities and references.
PRAGMA foreign_keys = OFF;

CREATE TABLE devices_v2 (
  id TEXT PRIMARY KEY,
  platform TEXT NOT NULL CHECK (platform IN ('ios', 'android', 'macos')),
  public_key_jwk TEXT NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  display_name TEXT,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  revoked_at INTEGER
);

INSERT INTO devices_v2
  (id, platform, public_key_jwk, fingerprint, display_name, created_at, last_seen_at, revoked_at)
SELECT id, platform, public_key_jwk, fingerprint, display_name, created_at, last_seen_at, revoked_at
FROM devices;

DROP TABLE devices;
ALTER TABLE devices_v2 RENAME TO devices;

PRAGMA foreign_keys = ON;

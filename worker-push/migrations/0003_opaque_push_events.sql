-- Permit opaque encrypted event metadata; real type/session remain ciphertext.
PRAGMA foreign_keys = OFF;
CREATE TABLE push_events_v2 (
  agent_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN ('approval_required', 'session_completed', 'encrypted')),
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
INSERT INTO push_events_v2 SELECT * FROM push_events;
DROP TABLE push_events;
ALTER TABLE push_events_v2 RENAME TO push_events;
CREATE INDEX push_events_created_idx ON push_events(created_at);
PRAGMA foreign_keys = ON;

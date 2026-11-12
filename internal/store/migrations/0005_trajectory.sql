-- Normalized trajectory records (Letta trajectory v1) for every agent.
-- The events row stays the source the extractors read. These rows are the
-- shared, model-ready transcript: one meta, then user, assistant, tool, and
-- observation records. Deleting an event removes its records.
CREATE TABLE trajectory_records (
  id INTEGER PRIMARY KEY,
  event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  session TEXT NOT NULL,
  project TEXT NOT NULL,
  agent TEXT NOT NULL,
  role TEXT NOT NULL,
  tool_call_id TEXT,
  ok INTEGER,
  ts INTEGER,
  record TEXT NOT NULL,
  digest TEXT NOT NULL UNIQUE
);
CREATE INDEX trajectory_session ON trajectory_records(session, id);

CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  agent TEXT NOT NULL,
  project TEXT NOT NULL,
  started INTEGER NOT NULL,
  last_event INTEGER NOT NULL,
  ended INTEGER,
  event_count INTEGER NOT NULL DEFAULT 0,
  last_consolidated_event_id INTEGER,
  consolidated_at INTEGER
);

CREATE TABLE events (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  stored_ts INTEGER NOT NULL,
  agent TEXT NOT NULL,
  session TEXT NOT NULL,
  project TEXT NOT NULL,
  kind TEXT NOT NULL,
  tool TEXT,
  payload TEXT,
  prompt_text TEXT,
  digest TEXT NOT NULL UNIQUE
);
CREATE INDEX events_session ON events(session, ts);
CREATE INDEX events_project_kind ON events(project, kind, ts);
CREATE INDEX events_ts ON events(ts);

CREATE TABLE memories (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  scope_kind TEXT NOT NULL,
  scope TEXT NOT NULL,
  key TEXT NOT NULL,
  body TEXT NOT NULL,
  text TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 1,
  confidence REAL NOT NULL,
  status TEXT NOT NULL,
  superseded_by TEXT,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  last_used INTEGER,
  UNIQUE(type, scope_kind, scope, key)
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
  text, key,
  content='memories', content_rowid='rowid',
  tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, text, key) VALUES (new.rowid, new.text, new.key);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, text, key) VALUES ('delete', old.rowid, old.text, old.key);
END;
CREATE TRIGGER memories_au AFTER UPDATE OF text, key ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, text, key) VALUES ('delete', old.rowid, old.text, old.key);
  INSERT INTO memories_fts(rowid, text, key) VALUES (new.rowid, new.text, new.key);
END;

CREATE TABLE memory_evidence (
  memory_id TEXT NOT NULL,
  event_id INTEGER NOT NULL,
  PRIMARY KEY(memory_id, event_id)
);
CREATE INDEX evidence_event ON memory_evidence(event_id);

CREATE TABLE replay_prompts (
  event_id INTEGER PRIMARY KEY,
  project TEXT,
  ts INTEGER,
  prompt_text TEXT
);

CREATE TABLE consolidation_runs (
  id TEXT PRIMARY KEY,
  session TEXT,
  agent TEXT,
  model TEXT,
  started INTEGER,
  finished INTEGER,
  status TEXT,
  error TEXT,
  ops TEXT
);

CREATE TABLE audit (
  id INTEGER PRIMARY KEY,
  run_id TEXT,
  op TEXT,
  memory_id TEXT,
  before TEXT,
  after TEXT,
  ts INTEGER
);

ALTER TABLE sessions ADD COLUMN transcript_path TEXT;
ALTER TABLE sessions ADD COLUMN usage_ingested_at INTEGER;
ALTER TABLE sessions ADD COLUMN usage_status TEXT;
CREATE TABLE injections (id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, session TEXT NOT NULL, agent TEXT NOT NULL, project TEXT NOT NULL, hook TEXT NOT NULL, tool TEXT NOT NULL DEFAULT '', line_count INTEGER NOT NULL, est_tokens INTEGER NOT NULL, memory_ids TEXT NOT NULL, holdout INTEGER NOT NULL DEFAULT 0, prompt_digest TEXT NOT NULL DEFAULT '');
CREATE INDEX injections_ts ON injections(project, ts);
CREATE INDEX injections_session ON injections(session, ts);
CREATE TABLE usage_turns (session TEXT NOT NULL, message_id TEXT NOT NULL, ts INTEGER NOT NULL, model TEXT NOT NULL, input INTEGER NOT NULL, output INTEGER NOT NULL, cache_read INTEGER NOT NULL, cache_creation INTEGER NOT NULL, PRIMARY KEY(session, message_id));
CREATE INDEX usage_turns_ts ON usage_turns(session, ts);
CREATE INDEX score_outcomes_ts ON score_outcomes(ts);

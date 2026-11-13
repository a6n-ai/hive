ALTER TABLE memories ADD COLUMN penalty REAL NOT NULL DEFAULT 0;
CREATE TABLE feedback_asks (id TEXT PRIMARY KEY, session TEXT NOT NULL, ts INTEGER NOT NULL, answered INTEGER NOT NULL DEFAULT 0);
CREATE TABLE feedback_ask_rows (ask_id TEXT NOT NULL, outcome_id INTEGER NOT NULL, PRIMARY KEY (ask_id, outcome_id));
CREATE INDEX feedback_asks_session ON feedback_asks(session, ts);
CREATE INDEX feedback_ask_rows_outcome ON feedback_ask_rows(outcome_id);

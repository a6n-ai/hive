-- One row each time a prompt injection is shown to an agent. The idle
-- score-model pass labels it from what the agent did afterwards and then
-- sets resolved. label stays null when the session ended with no signal.
CREATE TABLE score_outcomes (
  id INTEGER PRIMARY KEY,
  memory_id TEXT NOT NULL,
  session TEXT NOT NULL,
  agent TEXT NOT NULL DEFAULT '',
  ts INTEGER NOT NULL,
  features TEXT NOT NULL,
  score REAL NOT NULL,
  label INTEGER,
  resolved INTEGER
);
CREATE INDEX score_outcomes_open ON score_outcomes(resolved, session);

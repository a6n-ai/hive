ALTER TABLE score_outcomes ADD COLUMN prompt_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE score_outcomes ADD COLUMN shown_key TEXT NOT NULL DEFAULT '';
ALTER TABLE score_outcomes ADD COLUMN shown_text TEXT NOT NULL DEFAULT '';
ALTER TABLE score_outcomes ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE score_outcomes ADD COLUMN label_rule TEXT NOT NULL DEFAULT '';
ALTER TABLE score_outcomes ADD COLUMN scale REAL NOT NULL DEFAULT 1;
CREATE INDEX score_outcomes_group ON score_outcomes(session, prompt_digest);

ALTER TABLE memories ADD COLUMN forgotten_at INTEGER;
CREATE TRIGGER memories_forgotten_set AFTER UPDATE OF status ON memories
WHEN NEW.status = 'forgotten' AND OLD.status <> 'forgotten' BEGIN
  UPDATE memories SET forgotten_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE rowid = NEW.rowid;
END;
CREATE TRIGGER memories_forgotten_clear AFTER UPDATE OF status ON memories
WHEN NEW.status <> 'forgotten' AND OLD.status = 'forgotten' BEGIN
  UPDATE memories SET forgotten_at = NULL WHERE rowid = NEW.rowid;
END;

CREATE INDEX memory_deletions_id ON memory_deletions(id);

UPDATE score_outcomes SET resolved = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER),
  reason = 'closed_by_migration' WHERE resolved IS NULL;

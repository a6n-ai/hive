ALTER TABLE memories ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
UPDATE memories SET updated_at = last_seen;
CREATE INDEX memories_updated ON memories(updated_at);

CREATE TRIGGER memories_ins_updated AFTER INSERT ON memories BEGIN
  UPDATE memories SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE rowid = NEW.rowid;
END;
CREATE TRIGGER memories_upd_updated AFTER UPDATE ON memories BEGIN
  UPDATE memories SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE rowid = NEW.rowid;
END;

CREATE TABLE memory_deletions (id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE INDEX memory_deletions_ts ON memory_deletions(ts);
CREATE TRIGGER memories_del_logged AFTER DELETE ON memories BEGIN
  INSERT INTO memory_deletions(id, ts) VALUES (OLD.id, CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER));
END;

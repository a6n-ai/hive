DROP TRIGGER memories_upd_updated;
CREATE TRIGGER memories_upd_updated AFTER UPDATE OF type, scope_kind, scope, key, body, text, terms, count, confidence, status, superseded_by ON memories BEGIN
  UPDATE memories SET updated_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) WHERE rowid = NEW.rowid;
END;

-- The database's identity: a snapshot built from another database file is rejected.
INSERT OR IGNORE INTO meta(k, v) VALUES ('db_id', lower(hex(randomblob(16))));

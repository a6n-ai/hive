package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	sql     string
}

func migrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: name must start with NNNN_", e.Name())
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %q: %w", e.Name(), err)
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{v, string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func latestVersion() int {
	ms, err := migrations()
	if err != nil || len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// schemaVersion returns 0 for a fresh database. It takes a *sql.DB or *sql.Tx.
func schemaVersion(db interface {
	QueryRow(query string, args ...any) *sql.Row
}) (int, error) {
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var v string
	err := db.QueryRow(`SELECT v FROM meta WHERE k = 'schema_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

// migrate applies every pending migration in one transaction. Forward only.
func migrate(db *sql.DB) error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Another process may have migrated since the caller read the version.
	current, err := schemaVersion(tx)
	if err != nil {
		return err
	}
	applied := current
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		if _, err := tx.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %04d: %w", m.version, err)
		}
		applied = m.version
	}
	if applied == current {
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO meta(k, v) VALUES ('schema_version', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, strconv.Itoa(applied)); err != nil {
		return err
	}
	return tx.Commit()
}

// Package store owns Hive's SQLite database: schema, writes, and reads.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/a6n-ai/hive/model"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrLocked means another writer held the database past busy_timeout.
	ErrLocked = errors.New("hive: database locked")
	// ErrSchemaNewer means the database was migrated by a newer Hive; this
	// binary may read but not write.
	ErrSchemaNewer = errors.New("hive: database schema is newer than this binary; update hive")
	// ErrReadOnly means the store was opened read-only.
	ErrReadOnly = errors.New("hive: store opened read-only")
)

type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
	newer    bool
	hitLock  atomic.Bool
}

type Tx struct {
	tx          *sql.Tx
	spoolDir    string
	afterCommit []func()
}

type ListFilter struct {
	Types    []model.MemoryType
	Project  string
	Statuses []model.Status
	Key      string // exact key match when set
	Limit    int    // 0 means no limit
}

// Open opens or creates the database at path. The parent directory is created
// with mode 0700 and the file with mode 0600.
func Open(path string, readOnly bool) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if !readOnly {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(50)&_pragma=foreign_keys(1)&_txlock=immediate"
	if readOnly {
		dsn += "&mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, readOnly: readOnly}
	v, err := schemaVersion(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	switch {
	case v > latestVersion():
		s.newer = true
	case !readOnly && v < latestVersion():
		if err := migrate(db, v); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Path is the database file path.
func (s *Store) Path() string { return s.path }

// SchemaVersion is the migration level the database is at.
func (s *Store) SchemaVersion() (int, error) { return schemaVersion(s.db) }

func isBusy(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		c := se.Code() & 0xff
		return c == sqlite3.SQLITE_BUSY || c == sqlite3.SQLITE_LOCKED
	}
	return false
}

// Write runs fn in one BEGIN IMMEDIATE transaction. It returns ErrLocked when
// another writer holds the lock past busy_timeout.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	err := s.write(ctx, fn)
	if errors.Is(err, ErrLocked) {
		s.hitLock.Store(true)
	}
	return err
}

// HitLock reports whether a write by this store has returned ErrLocked. A
// process that has waited on the lock once should not wait on it again.
func (s *Store) HitLock() bool { return s.hitLock.Load() }

func (s *Store) write(ctx context.Context, fn func(*Tx) error) error {
	if s.newer {
		return ErrSchemaNewer
	}
	if s.readOnly {
		return ErrReadOnly
	}
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if isBusy(err) {
			return ErrLocked
		}
		return err
	}
	tx := &Tx{tx: sqlTx, spoolDir: s.spoolDir()}
	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback()
		if isBusy(err) {
			return ErrLocked
		}
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		if isBusy(err) {
			return ErrLocked
		}
		return err
	}
	for _, f := range tx.afterCommit {
		f()
	}
	return nil
}

// AfterCommit registers f to run only if the transaction commits.
func (t *Tx) AfterCommit(f func()) { t.afterCommit = append(t.afterCommit, f) }

func nowMs() int64 { return time.Now().UnixMilli() }

func marshal(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func unmarshal(s sql.NullString) map[string]any {
	out := map[string]any{}
	if s.Valid && s.String != "" {
		_ = json.Unmarshal([]byte(s.String), &out)
	}
	return out
}

// InsertEvent inserts e unless an event with the same digest exists. It sets
// e.ID on insert.
func (t *Tx) InsertEvent(e *model.Event) (bool, error) {
	payload, err := marshal(e.Payload)
	if err != nil {
		return false, err
	}
	res, err := t.tx.Exec(`INSERT INTO events(ts, stored_ts, agent, session, project, kind, tool, payload, prompt_text, digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(digest) DO NOTHING`,
		e.TS, nowMs(), e.Agent, e.Session, e.Project, string(e.Kind), nullable(e.Tool), payload, nullable(e.PromptText), e.Digest)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return false, err
	}
	e.ID = id
	return true, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "m_" + hex.EncodeToString(b)
}

// ShallowMerge overlays next onto prev; it is UpsertMemory's default merge.
func ShallowMerge(prev, next map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range prev {
		out[k] = v
	}
	for k, v := range next {
		out[k] = v
	}
	return out
}

// UpsertMemory inserts m, or on a matching (type, scope_kind, scope, key)
// increments count, sets last_seen, merges body (merge nil = shallow overlay),
// and raises confidence to at least 1 - 0.5/count. It always records the
// evidence event when evidenceEventID > 0.
func (t *Tx) UpsertMemory(m *model.Memory, merge func(prev, next map[string]any) map[string]any, evidenceEventID int64) (*model.Memory, error) {
	if merge == nil {
		merge = ShallowMerge
	}
	existing, err := scanMemory(t.tx.QueryRow(memorySelect+` WHERE type=? AND scope_kind=? AND scope=? AND key=?`,
		string(m.Type), string(m.ScopeKind), m.Scope, m.Key))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		m.ID = newID()
		if m.Count == 0 {
			m.Count = 1
		}
		m.Confidence = maxF(m.Confidence, countConfidence(m.Count))
		body, err := marshal(m.Body)
		if err != nil {
			return nil, err
		}
		if _, err := t.tx.Exec(`INSERT INTO memories(id, type, scope_kind, scope, key, body, text, count, confidence, status,
			superseded_by, first_seen, last_seen, last_used) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			m.ID, string(m.Type), string(m.ScopeKind), m.Scope, m.Key, body, m.Text, m.Count, m.Confidence, string(m.Status),
			nullable(m.SupersededBy), m.FirstSeen, m.LastSeen, nullInt(m.LastUsed)); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		existing.Count++
		existing.LastSeen = m.LastSeen
		existing.Body = merge(existing.Body, m.Body)
		existing.Text = m.Text
		existing.Confidence = maxF(maxF(existing.Confidence, m.Confidence), countConfidence(existing.Count))
		body, err := marshal(existing.Body)
		if err != nil {
			return nil, err
		}
		if _, err := t.tx.Exec(`UPDATE memories SET count=?, last_seen=?, body=?, text=?, confidence=? WHERE id=?`,
			existing.Count, existing.LastSeen, body, existing.Text, existing.Confidence, existing.ID); err != nil {
			return nil, err
		}
		m = existing
	}
	if evidenceEventID > 0 {
		if _, err := t.tx.Exec(`INSERT OR IGNORE INTO memory_evidence(memory_id, event_id) VALUES (?, ?)`, m.ID, evidenceEventID); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func countConfidence(count int) float64 {
	if count <= 0 {
		return 0
	}
	return 1 - 0.5/float64(count)
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// TouchSession records that e happened in its session.
func (t *Tx) TouchSession(e *model.Event) error {
	ended := e.Kind == model.SessionEnd
	_, err := t.tx.Exec(`INSERT INTO sessions(id, agent, project, started, last_event, event_count, ended)
		VALUES (?, ?, ?, ?, ?, 1, CASE WHEN ? THEN ? END)
		ON CONFLICT(id) DO UPDATE SET last_event = excluded.last_event, event_count = event_count + 1,
		ended = CASE WHEN ? THEN excluded.last_event ELSE ended END`,
		e.Session, e.Agent, e.Project, e.TS, e.TS, ended, e.TS, ended)
	return err
}

const memorySelect = `SELECT id, type, scope_kind, scope, key, body, text, count, confidence, status,
	superseded_by, first_seen, last_seen, last_used FROM memories`

type rowScanner interface{ Scan(dest ...any) error }

func scanMemory(r rowScanner) (*model.Memory, error) { return scanMemoryWith(r) }

// filterSQL builds a WHERE fragment over table alias a.
func filterSQL(a string, f ListFilter) (string, []any) {
	var conds []string
	var args []any
	if len(f.Types) > 0 {
		conds = append(conds, a+".type IN ("+placeholders(len(f.Types))+")")
		for _, t := range f.Types {
			args = append(args, string(t))
		}
	}
	if len(f.Statuses) > 0 {
		conds = append(conds, a+".status IN ("+placeholders(len(f.Statuses))+")")
		for _, s := range f.Statuses {
			args = append(args, string(s))
		}
	}
	if f.Key != "" {
		conds = append(conds, a+".key = ?")
		args = append(args, f.Key)
	}
	if f.Project != "" {
		conds = append(conds, "("+a+".scope_kind = 'global' OR "+a+".scope = ? OR ("+a+".scope_kind = 'file' AND "+a+".scope LIKE ? ESCAPE '\\'))")
		args = append(args, f.Project, escapeLike(f.Project)+"/%")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(conds, " AND "), args
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// GetMemory returns one memory by id.
func (s *Store) GetMemory(id string) (*model.Memory, error) {
	return scanMemory(s.db.QueryRow(memorySelect+` WHERE id = ?`, id))
}

// ListMemories returns memories matching f, most recently seen first.
func (s *Store) ListMemories(f ListFilter) ([]model.Memory, error) {
	where, args := filterSQL("memories", f)
	q := memorySelect + ` WHERE 1=1` + where + ` ORDER BY last_seen DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// SearchFTS searches memory text and key. Each term is matched as a prefix and
// terms are ORed. Scores are SQLite bm25 values (lower is better).
func (s *Store) SearchFTS(terms []string, f ListFilter, limit int) ([]model.Memory, []float64, error) {
	var q []string
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		q = append(q, `"`+strings.ReplaceAll(t, `"`, `""`)+`"*`)
	}
	if len(q) == 0 {
		return nil, nil, nil
	}
	where, args := filterSQL("m", f)
	query := `SELECT m.id, m.type, m.scope_kind, m.scope, m.key, m.body, m.text, m.count, m.confidence, m.status,
		m.superseded_by, m.first_seen, m.last_seen, m.last_used, bm25(memories_fts)
		FROM memories_fts JOIN memories m ON m.rowid = memories_fts.rowid
		WHERE memories_fts MATCH ?` + where + ` ORDER BY bm25(memories_fts) LIMIT ?`
	all := append([]any{strings.Join(q, " OR ")}, args...)
	all = append(all, limit)
	rows, err := s.db.Query(query, all...)
	if err != nil {
		return nil, nil, fmt.Errorf("fts search: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ms []model.Memory
	var scores []float64
	for rows.Next() {
		var score float64
		m, err := scanMemoryWith(rows, &score)
		if err != nil {
			return nil, nil, err
		}
		ms = append(ms, *m)
		scores = append(scores, score)
	}
	return ms, scores, rows.Err()
}

func scanMemoryWith(r rowScanner, extra ...any) (*model.Memory, error) {
	var m model.Memory
	var typ, sk, status string
	var body, sup sql.NullString
	var lastUsed sql.NullInt64
	dest := append([]any{&m.ID, &typ, &sk, &m.Scope, &m.Key, &body, &m.Text, &m.Count, &m.Confidence, &status,
		&sup, &m.FirstSeen, &m.LastSeen, &lastUsed}, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	m.Type, m.ScopeKind, m.Status = model.MemoryType(typ), model.ScopeKind(sk), model.Status(status)
	m.Body = unmarshal(body)
	m.SupersededBy = sup.String
	m.LastUsed = lastUsed.Int64
	return &m, nil
}

const eventSelect = `SELECT e.id, e.ts, e.agent, e.session, e.project, e.kind, e.tool, e.payload, e.prompt_text, e.digest FROM events e`

func scanEvents(rows *sql.Rows) ([]model.Event, error) {
	defer func() { _ = rows.Close() }()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var kind string
		var tool, payload, prompt sql.NullString
		if err := rows.Scan(&e.ID, &e.TS, &e.Agent, &e.Session, &e.Project, &kind, &tool, &payload, &prompt, &e.Digest); err != nil {
			return nil, err
		}
		e.Kind, e.Tool, e.PromptText = model.EventKind(kind), tool.String, prompt.String
		e.Payload = unmarshal(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Evidence returns the events recorded as evidence for a memory, oldest first.
func (s *Store) Evidence(memoryID string) ([]model.Event, error) {
	rows, err := s.db.Query(eventSelect+` JOIN memory_evidence me ON me.event_id = e.id WHERE me.memory_id = ? ORDER BY e.ts, e.id`, memoryID)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// QueryEvents returns events matching every non-zero filter, oldest first.
func (s *Store) QueryEvents(session, project string, kinds []model.EventKind, since, until int64, limit int) ([]model.Event, error) {
	var conds []string
	var args []any
	if session != "" {
		conds, args = append(conds, "e.session = ?"), append(args, session)
	}
	if project != "" {
		conds, args = append(conds, "e.project = ?"), append(args, project)
	}
	if len(kinds) > 0 {
		conds = append(conds, "e.kind IN ("+placeholders(len(kinds))+")")
		for _, k := range kinds {
			args = append(args, string(k))
		}
	}
	if since > 0 {
		conds, args = append(conds, "e.ts >= ?"), append(args, since)
	}
	if until > 0 {
		conds, args = append(conds, "e.ts <= ?"), append(args, until)
	}
	q := eventSelect
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY e.ts, e.id"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// SessionEvents returns a session's events, oldest first.
func (s *Store) SessionEvents(session string) ([]model.Event, error) {
	rows, err := s.db.Query(eventSelect+` WHERE e.session = ? ORDER BY e.ts, e.id`, session)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// SessionEvents inside a transaction sees the transaction's own writes.
func (t *Tx) SessionEvents(session string) ([]model.Event, error) {
	rows, err := t.tx.Query(eventSelect+` WHERE e.session = ? ORDER BY e.ts, e.id`, session)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// LastEvent returns the session's latest event with id below beforeID (0 means
// no bound), or nil when there is none.
func (t *Tx) LastEvent(session string, beforeID int64) (*model.Event, error) {
	q := eventSelect + ` WHERE e.session = ?`
	args := []any{session}
	if beforeID > 0 {
		q += ` AND e.id < ?`
		args = append(args, beforeID)
	}
	rows, err := t.tx.Query(q+` ORDER BY e.ts DESC, e.id DESC LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	evs, err := scanEvents(rows)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	return &evs[0], nil
}

// RecentEvents returns up to limit of the session's events with id below
// beforeID, newest first. Extractors use it to look back without scanning a
// whole session.
func (t *Tx) RecentEvents(session string, beforeID int64, limit int) ([]model.Event, error) {
	rows, err := t.tx.Query(eventSelect+` WHERE e.session = ? AND e.id < ? ORDER BY e.id DESC LIMIT ?`, session, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// AddEvidence links an event to a memory; duplicates are ignored.
func (t *Tx) AddEvidence(memoryID string, eventID int64) error {
	_, err := t.tx.Exec(`INSERT OR IGNORE INTO memory_evidence(memory_id, event_id) VALUES (?, ?)`, memoryID, eventID)
	return err
}

// SetStatus changes a memory's status.
func (t *Tx) SetStatus(memoryID string, s model.Status) error {
	_, err := t.tx.Exec(`UPDATE memories SET status = ? WHERE id = ?`, string(s), memoryID)
	return err
}

// DeleteMemory removes a memory and the evidence links that point at it.
func (t *Tx) DeleteMemory(id string) error {
	if _, err := t.tx.Exec(`DELETE FROM memory_evidence WHERE memory_id = ?`, id); err != nil {
		return err
	}
	_, err := t.tx.Exec(`DELETE FROM memories WHERE id = ?`, id)
	return err
}

// TouchUsed sets last_used for the given memories.
func (t *Tx) TouchUsed(ids []string, ts int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{ts}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := t.tx.Exec(`UPDATE memories SET last_used = ? WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

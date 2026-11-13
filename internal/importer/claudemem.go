package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe/internal/capture"
	"github.com/a6n-ai/inscribe/model"
	_ "modernc.org/sqlite"
)

// ClaudeMemOptions selects claude-mem observations. DB defaults to ~/.claude-mem/claude-mem.db
// and Types to decision and bugfix. Project matches the git root of the session cwd.
type ClaudeMemOptions struct {
	DB      string
	Types   []string
	Project string
	Since   time.Time
	DryRun  bool
}

// ClaudeMemReport counts observations read, events produced, and skips by reason.
// Imported is the number of events handed to capture, or that would be with DryRun.
type ClaudeMemReport struct {
	Read, Imported, Skipped int
	SkippedByReason         map[string]int
}

// claudeMemTypes maps claude-mem observation types to Inscribe memory types.
var claudeMemTypes = map[string]string{"decision": "decision", "bugfix": "fix"}

const claudeMemConfidence = 0.6

// ReadClaudeMem reads claude-mem observations without writing to the database
// and returns them as remembered events.
func ReadClaudeMem(ctx context.Context, o ClaudeMemOptions) ([]model.Event, ClaudeMemReport, error) {
	rep := ClaudeMemReport{SkippedByReason: map[string]int{}}
	types := o.Types
	if len(types) == 0 {
		types = []string{"decision", "bugfix"}
	}
	want := map[string]bool{}
	for _, t := range types {
		if _, ok := claudeMemTypes[t]; !ok {
			return nil, rep, fmt.Errorf("claude-mem type %q has no Inscribe mapping", t)
		}
		want[t] = true
	}
	if o.DB == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, rep, err
		}
		o.DB = filepath.Join(home, ".claude-mem", "claude-mem.db")
	}
	if o.Project != "" {
		o.Project = capture.ResolveProject(o.Project)
	}
	projects := map[string]string{}
	resolve := func(cwd string) string {
		if cwd == "" {
			return ""
		}
		p, ok := projects[cwd]
		if !ok {
			p = capture.ResolveProject(cwd)
			projects[cwd] = p
		}
		return p
	}
	db, err := sql.Open("sqlite", "file:"+o.DB+"?mode=ro")
	if err != nil {
		return nil, rep, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT o.type, o.title, o.facts, o.files_read, o.files_modified, o.created_at_epoch, s.cwd
		FROM observations o LEFT JOIN sdk_sessions s ON s.memory_session_id = o.memory_session_id ORDER BY o.id`)
	if err != nil {
		return nil, rep, err
	}
	defer func() { _ = rows.Close() }()

	var evs []model.Event
	for rows.Next() {
		rep.Read++
		var typ, title, facts, read, modified, cwd sql.NullString
		var epoch sql.NullInt64
		if err := rows.Scan(&typ, &title, &facts, &read, &modified, &epoch, &cwd); err != nil {
			return nil, rep, err
		}
		ts := epoch.Int64
		if ts != 0 && ts < 1e11 { // claude-mem stores seconds in older rows
			ts *= 1000
		}
		project := resolve(cwd.String)
		reason := claudeMemSkip(o, want, claudeMemRow{typ: typ.String, title: title.String, paths: read.String + "\n" + modified.String, cwd: cwd.String, project: project, ts: ts})
		if reason != "" {
			rep.SkippedByReason[reason]++
			continue
		}
		text := title.String
		var list []string
		if json.Unmarshal([]byte(facts.String), &list) == nil && len(list) > 0 && list[0] != "" {
			text += ". " + list[0]
		}
		evs = append(evs, model.Event{TS: ts, Agent: "claude-mem", Session: "claude-mem-import", Project: project,
			Kind: model.Remembered, Payload: map[string]any{"text": text, "type": claudeMemTypes[typ.String],
				"scope": "project", "confidence": claudeMemConfidence}})
	}
	if err := rows.Err(); err != nil {
		return nil, rep, err
	}
	rep.Imported = len(evs)
	rep.Skipped = rep.Read - rep.Imported
	return evs, rep, nil
}

type claudeMemRow struct {
	typ, title, paths, cwd, project string
	ts                              int64
}

// claudeMemSkip returns why an observation is not imported, or "" when it is.
func claudeMemSkip(o ClaudeMemOptions, want map[string]bool, r claudeMemRow) string {
	switch {
	case r.cwd == "":
		return "no_cwd"
	case r.typ == "sensitive":
		return "sensitive"
	case strings.Contains(r.paths, "apps/puchkaman") || strings.Contains(r.cwd, "apps/puchkaman"):
		return "puchkaman"
	case r.ts == 0:
		return "no_time"
	case r.title == "":
		return "empty_title"
	case !want[r.typ]:
		return "type"
	case o.Project != "" && r.project != o.Project:
		return "project"
	case !o.Since.IsZero() && r.ts < o.Since.UnixMilli():
		return "since"
	}
	return ""
}

package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/feedback"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

func openInscribe(t *testing.T, proj string) *inscribe.Inscribe {
	t.Helper()
	return openAt(t, t.TempDir(), proj)
}

func openAt(t *testing.T, home, proj string) *inscribe.Inscribe {
	t.Helper()
	h, err := inscribe.Open(inscribe.Options{Home: home, Caller: "test", Project: proj})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func connect(t *testing.T, h *inscribe.Inscribe, project string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := New(h, project).Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call returns the text of a tool call and whether the tool reported an error.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

func TestMCPRememberRecall(t *testing.T) {
	proj := t.TempDir()
	cs := connect(t, openInscribe(t, proj), proj)
	id, isErr := call(t, cs, "remember", map[string]any{"text": "use make deploy for releases", "type": "note", "scope": "project", "project": proj})
	if isErr || strings.TrimSpace(id) == "" {
		t.Fatalf("remember = %q, error %v", id, isErr)
	}
	out, isErr := call(t, cs, "recall", map[string]any{"query": "deploy", "project": proj})
	if isErr || !strings.Contains(out, "use make deploy for releases") {
		t.Fatalf("recall = %q, error %v", out, isErr)
	}
}

func TestMCPForgetNeverPurges(t *testing.T) {
	proj := t.TempDir()
	h := openInscribe(t, proj)
	cs := connect(t, h, proj)
	id, _ := call(t, cs, "remember", map[string]any{"text": "deploys go through make deploy", "type": "note", "scope": "project", "project": proj})
	status, isErr := call(t, cs, "forget", map[string]any{"id": strings.TrimSpace(id)})
	if isErr || !strings.Contains(status, "forgotten") {
		t.Fatalf("forget = %q, error %v", status, isErr)
	}
	left, err := h.List(context.Background(), inscribe.ListFilter{Project: proj, Statuses: []model.Status{model.Forgotten}})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].ID != strings.TrimSpace(id) {
		t.Fatalf("forgotten memory was purged: %v", left)
	}
}

func TestMCPBadArgs(t *testing.T) {
	cs := connect(t, openInscribe(t, t.TempDir()), t.TempDir())
	long := strings.Repeat("x", 5000)
	cases := map[string]struct {
		tool string
		args map[string]any
	}{
		"missing text":      {"remember", map[string]any{"type": "note"}},
		"text too long":     {"remember", map[string]any{"text": long, "type": "note"}},
		"negative budget":   {"recall", map[string]any{"query": "x", "budget": -1}},
		"budget too large":  {"recall", map[string]any{"query": "x", "budget": 5000}},
		"wrong type":        {"recall", map[string]any{"budget": "lots"}},
		"unknown id forget": {"forget", map[string]any{"id": "does-not-exist"}},
		"missing id why":    {"why", map[string]any{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, isErr := call(t, cs, c.tool, c.args); !isErr {
				t.Fatal("bad arguments accepted")
			}
		})
	}
}

func TestMCPScrubsRemember(t *testing.T) {
	proj := t.TempDir()
	cs := connect(t, openInscribe(t, proj), proj)
	secret := "AKIAABCDEFGHIJKLMNOP"
	if _, isErr := call(t, cs, "remember", map[string]any{"text": "deploy key " + secret + " lives in ci", "type": "note", "scope": "project", "project": proj}); isErr {
		t.Fatal("remember failed")
	}
	out, _ := call(t, cs, "recall", map[string]any{"query": "deploy key", "project": proj})
	if strings.Contains(out, secret) {
		t.Fatalf("secret recalled: %q", out)
	}
}

func TestMCPRecallDefaultsToServerProject(t *testing.T) {
	projA, projB := t.TempDir(), t.TempDir()
	h := openInscribe(t, projA)
	ctx := context.Background()
	if _, err := h.Remember(ctx, "projA uses make deploy", model.Note, model.ProjectScope, projA); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Remember(ctx, "projB secret is banana", model.Note, model.ProjectScope, projB); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, h, projA)
	out, isErr := call(t, cs, "recall", map[string]any{})
	if isErr || !strings.Contains(out, "projA uses make deploy") || strings.Contains(out, "banana") {
		t.Fatalf("recall = %q, error %v", out, isErr)
	}
	if _, isErr := call(t, cs, "recall", map[string]any{"project": projB}); !isErr {
		t.Fatal("recall accepted another project")
	}
}

func TestMCPForgetWhyRefuseOtherProject(t *testing.T) {
	projA, projB := t.TempDir(), t.TempDir()
	h := openInscribe(t, projA)
	id, err := h.Remember(context.Background(), "projB secret is banana", model.Note, model.ProjectScope, projB)
	if err != nil {
		t.Fatal(err)
	}
	cs := connect(t, h, projA)
	for _, tool := range []string{"forget", "why"} {
		if _, isErr := call(t, cs, tool, map[string]any{"id": id}); !isErr {
			t.Fatalf("%s acted on another project's memory", tool)
		}
	}
}

func TestMCPRememberDefaultsToProject(t *testing.T) {
	projA := t.TempDir()
	h := openInscribe(t, projA)
	cs := connect(t, h, projA)
	if _, isErr := call(t, cs, "remember", map[string]any{"text": "deploy with make deploy"}); isErr {
		t.Fatal("remember failed")
	}
	ms, err := h.List(context.Background(), inscribe.ListFilter{Project: projA})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ScopeKind == model.ProjectScope && m.Scope == projA {
			return
		}
	}
	t.Fatalf("remember without scope did not default to project scope: %v", ms)
}

func TestMCPQueryAndProjectCapped(t *testing.T) {
	proj := t.TempDir()
	cs := connect(t, openInscribe(t, proj), proj)
	cases := map[string]map[string]any{
		"long query":   {"query": strings.Repeat("x", 2001)},
		"long project": {"project": strings.Repeat("p", 4097)},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, isErr := call(t, cs, "recall", args); !isErr {
				t.Fatal("oversized argument accepted")
			}
		})
	}
}

func TestMCPReachesRepoInsideWorkspace(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := openInscribe(t, workspace)
	cs := connect(t, h, workspace)
	if _, isErr := call(t, cs, "remember", map[string]any{"text": "repo deploys with make ship", "project": filepath.Join(repo, "sub")}); isErr {
		t.Fatal("remember refused a repo inside the workspace")
	}
	if _, err := h.Remember(context.Background(), "shared tip: prefer rg", model.Preference, model.Global, ""); err != nil {
		t.Fatal(err)
	}
	out, isErr := call(t, cs, "recall", map[string]any{"project": repo})
	if isErr || !strings.Contains(out, "make ship") || !strings.Contains(out, "prefer rg") {
		t.Fatalf("recall = %q, error %v", out, isErr)
	}
	if out, isErr := call(t, cs, "recall", map[string]any{"project": "repo/sub"}); isErr || !strings.Contains(out, "make ship") {
		t.Fatalf("recall with a path relative to the workspace = %q, error %v", out, isErr)
	}
	ms, err := h.List(context.Background(), inscribe.ListFilter{Project: repo})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, m := range ms {
		if m.Scope == repo {
			id = m.ID
		}
	}
	for _, tool := range []string{"why", "forget"} {
		if out, isErr := call(t, cs, tool, map[string]any{"id": id}); isErr {
			t.Fatalf("%s refused a memory in a repo inside the workspace: %s", tool, out)
		}
	}
	if _, isErr := call(t, cs, "recall", map[string]any{"project": filepath.Dir(workspace)}); !isErr {
		t.Fatal("recall accepted a parent of the server project")
	}
	if _, isErr := call(t, cs, "recall", map[string]any{"project": "repo/../.."}); !isErr {
		t.Fatal("recall accepted a relative path that climbs out of the server project")
	}
}

func TestMCPToolsAnnotatedForAutoMode(t *testing.T) {
	proj := t.TempDir()
	cs := connect(t, openInscribe(t, proj), proj)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{"recall": true, "why": true, "remember": false, "forget": false}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint || a.ReadOnlyHint != readOnly[tool.Name] {
			t.Fatalf("%s annotations = %+v", tool.Name, a)
		}
		if !a.ReadOnlyHint && (a.DestructiveHint == nil || *a.DestructiveHint) {
			t.Fatalf("%s is marked destructive", tool.Name)
		}
	}
}

func TestEvidenceLine(t *testing.T) {
	cases := []struct {
		e    model.Event
		want string
	}{
		{model.Event{Kind: "prompt", PromptText: "use rg"}, "[prompt] use rg"},
		{model.Event{Kind: "post_tool", Tool: "shell", Payload: map[string]any{"command": "make build", "exit_code": float64(2), "error": "no rule"}}, "[post_tool] shell make build (exit 2) error: no rule"},
		{model.Event{Kind: "post_tool", Tool: "edit", Payload: map[string]any{"path": "a.go"}}, "[post_tool] edit a.go"},
		{model.Event{Kind: "post_tool", Tool: "web", Payload: map[string]any{"url": "x.dev"}}, `[post_tool] web {"url":"x.dev"}`},
		{model.Event{Kind: "session_start"}, "[session_start]"},
	}
	for _, c := range cases {
		if got := evidenceLine(c.e); got != c.want {
			t.Errorf("evidenceLine = %q, want %q", got, c.want)
		}
	}
	long := evidenceLine(model.Event{Kind: "prompt", PromptText: strings.Repeat("é", 500)})
	if n := len([]rune(long)); n != maxEvidence+1 || !strings.HasSuffix(long, "…") {
		t.Errorf("long line has %d runes, want %d ending in …", n, maxEvidence+1)
	}
}

// rememberAll stores one project memory per text and returns their ids.
func rememberAll(t *testing.T, h *inscribe.Inscribe, proj string, texts ...string) []string {
	t.Helper()
	var ids []string
	for _, text := range texts {
		id, err := h.Remember(context.Background(), text, model.Note, model.ProjectScope, proj)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// askRound injects the memories matching "deploy make" into one prompt of
// session, then asks about them. It returns the ask id.
func askRound(t *testing.T, h *inscribe.Inscribe, proj, session, digest string, now time.Time) string {
	t.Helper()
	ctx := context.Background()
	if err := h.Capture(ctx, inscribe.Event{Agent: "codex", Session: session, Project: proj, Kind: model.SessionStart, TS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Recall(ctx, inscribe.Query{Project: proj, Text: "deploy make target", Agent: "codex", Session: session, PromptDigest: digest, Budget: maxBudget, Mode: recall.Prompt}); err != nil {
		t.Fatal(err)
	}
	if err := h.MarkMCPServed(ctx, now); err != nil {
		t.Fatal(err)
	}
	askID, _, _, err := h.MaybeAsk(ctx, session, true, now)
	if err != nil || askID == "" {
		t.Fatalf("MaybeAsk = %q, %v; want an ask", askID, err)
	}
	return askID
}

type outcome struct {
	Memory   string
	Label    sql.NullInt64
	Reason   sql.NullString
	Scale    float64
	Resolved sql.NullInt64
}

// outcomes reads the score_outcomes rows of the store under home, oldest first.
func outcomes(t *testing.T, home string) []outcome {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "inscribe.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT memory_id, label, reason, scale, resolved FROM score_outcomes ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []outcome
	for rows.Next() {
		var o outcome
		if err := rows.Scan(&o.Memory, &o.Label, &o.Reason, &o.Scale, &o.Resolved); err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// execRaw runs a write against the store under home, for states no tool reaches yet.
func execRaw(t *testing.T, home, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "inscribe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// feedbackHome is a store home whose recall injects up to ten lines per prompt,
// enough for an ask to cover four memories.
func feedbackHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[recall]\nprompt_max_lines = 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// An ask reaches only an agent whose inscribe MCP server has run, so a save
// before that asks nothing.
func TestMCPFeedbackNoAskBeforeServerRuns(t *testing.T) {
	proj := t.TempDir()
	h := openAt(t, feedbackHome(t), proj)
	rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	ctx := context.Background()
	now := time.Now()
	if err := h.Capture(ctx, inscribe.Event{Agent: "codex", Session: "s1", Project: proj, Kind: model.SessionStart, TS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Recall(ctx, inscribe.Query{Project: proj, Text: "deploy make target", Agent: "codex", Session: "s1", PromptDigest: "d1", Budget: maxBudget, Mode: recall.Prompt}); err != nil {
		t.Fatal(err)
	}
	if askID, _, _, err := h.MaybeAsk(ctx, "s1", true, now); err != nil || askID != "" {
		t.Fatalf("MaybeAsk before the MCP server ran = %q, %v; want no ask", askID, err)
	}
	if err := h.MarkMCPServed(ctx, now); err != nil {
		t.Fatal(err)
	}
	if askID, _, _, err := h.MaybeAsk(ctx, "s1", true, now); err != nil || askID == "" {
		t.Fatalf("MaybeAsk after the MCP server ran = %q, %v; want an ask", askID, err)
	}
}

func feedbackItems(items ...map[string]any) map[string]any {
	return map[string]any{"items": items}
}

func TestMCPFeedbackLabels(t *testing.T) {
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	cs := connect(t, h, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	args := feedbackItems(
		map[string]any{"id": ids[0], "used": "yes", "helpful": "yes", "how": "ran make deploy"},
		map[string]any{"id": ids[1], "used": "partly", "helpful": "yes", "how": "half of it"},
		map[string]any{"id": ids[2], "used": "no", "helpful": "no"},
		map[string]any{"id": ids[3], "used": "no", "helpful": "wrong"},
	)
	args["ask"] = askID
	out, isErr := call(t, cs, "feedback", args)
	if isErr || out != "labeled 4 rows, 1 wrong" {
		t.Fatalf("feedback = %q, error %v", out, isErr)
	}

	want := map[string]struct {
		label  int64
		scale  float64
		reason string
	}{
		ids[0]: {1, 0.5, "agent"},
		ids[1]: {1, 0.25, "agent"},
		ids[2]: {0, 0.5, "agent"},
		ids[3]: {0, 0.5, "agent_wrong"},
	}
	rows := outcomes(t, home)
	if len(rows) != 4 {
		t.Fatalf("outcome rows = %d, want 4", len(rows))
	}
	for _, o := range rows {
		w, ok := want[o.Memory]
		if !ok || o.Label.Int64 != w.label || o.Reason.String != w.reason || math.Abs(o.Scale-w.scale) > 1e-9 || !o.Resolved.Valid {
			t.Fatalf("row %+v, want label %d scale %v reason %q", o, w.label, w.scale, w.reason)
		}
	}

	penalty := map[string]float64{}
	mems, err := h.List(context.Background(), inscribe.ListFilter{Project: proj})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mems {
		penalty[m.ID] = m.Penalty
	}
	if math.Abs(penalty[ids[3]]-0.2) > 1e-9 || penalty[ids[0]] != 0 || penalty[ids[1]] != 0 || penalty[ids[2]] != 0 {
		t.Fatalf("penalties = %v, want 0.2 on the wrong memory only", penalty)
	}
}

func TestMCPFeedbackTwiceRefused(t *testing.T) {
	proj := t.TempDir()
	h := openAt(t, feedbackHome(t), proj)
	cs := connect(t, h, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	args := feedbackItems(map[string]any{"id": ids[0], "used": "no", "helpful": "no"})
	args["ask"] = askID
	if _, isErr := call(t, cs, "feedback", args); isErr {
		t.Fatal("first answer refused")
	}
	if _, isErr := call(t, cs, "feedback", args); !isErr {
		t.Fatal("second answer to the same ask accepted")
	}
}

func TestMCPFeedbackSkipsResolvedRows(t *testing.T) {
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	cs := connect(t, h, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	execRaw(t, home, `UPDATE score_outcomes SET label = 1, reason = 'full_command', scale = 1, resolved = 1 WHERE memory_id = ?`, ids[0])

	args := feedbackItems(map[string]any{"id": ids[0], "used": "no", "helpful": "wrong"})
	args["ask"] = askID
	out, isErr := call(t, cs, "feedback", args)
	if isErr || out != "labeled 0 rows, 1 wrong" {
		t.Fatalf("feedback = %q, error %v", out, isErr)
	}
	for _, o := range outcomes(t, home) {
		if o.Memory == ids[0] && (o.Label.Int64 != 1 || o.Reason.String != "full_command" || o.Scale != 1) {
			t.Fatalf("resolved row changed: %+v", o)
		}
	}
}

func TestMCPFeedbackOtherProjectRefused(t *testing.T) {
	projA, projB := t.TempDir(), t.TempDir()
	inner := filepath.Join(projA, "inner")
	home := feedbackHome(t)
	h := openAt(t, home, projA)
	cs := connect(t, h, projA)
	now := time.Now()

	memB := rememberAll(t, h, projB, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askB := askRound(t, h, projB, "sB", "d1", now)
	args := feedbackItems(map[string]any{"id": memB[0], "used": "no", "helpful": "wrong"})
	args["ask"] = askB
	if _, isErr := call(t, cs, "feedback", args); !isErr {
		t.Fatal("ask of another project accepted")
	}
	for _, o := range outcomes(t, home) {
		if o.Resolved.Valid || o.Label.Valid {
			t.Fatalf("row of another project changed: %+v", o)
		}
	}

	ids := rememberAll(t, h, inner, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askI := askRound(t, h, inner, "sI", "d1", now)
	args = feedbackItems(map[string]any{"id": ids[0], "used": "no", "helpful": "no"})
	args["ask"] = askI
	if out, isErr := call(t, cs, "feedback", args); isErr {
		t.Fatalf("ask of a repository inside the server project refused: %s", out)
	}
}

func TestMCPFeedbackBadItemWritesNothing(t *testing.T) {
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	cs := connect(t, h, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	args := feedbackItems(
		map[string]any{"id": ids[0], "used": "no", "helpful": "wrong"},
		map[string]any{"id": "not-in-ask", "used": "yes", "helpful": "yes"},
	)
	args["ask"] = askID
	if _, isErr := call(t, cs, "feedback", args); !isErr {
		t.Fatal("item outside the ask accepted")
	}
	for _, o := range outcomes(t, home) {
		if o.Resolved.Valid || o.Label.Valid {
			t.Fatalf("row written by a refused call: %+v", o)
		}
	}
	args = feedbackItems(map[string]any{"id": ids[0], "used": "no", "helpful": "wrong"})
	args["ask"] = askID
	if _, isErr := call(t, cs, "feedback", args); isErr {
		t.Fatal("ask was answered by the refused call")
	}
}

// Each refused call pairs a valid first item with one bad item, so a partial
// write would show. The ask must stay open for the valid call that follows.
func TestMCPFeedbackRefusedItemsWriteNothing(t *testing.T) {
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	cs := connect(t, h, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	valid := map[string]any{"id": ids[0], "used": "no", "helpful": "wrong"}
	bad := map[string]map[string]any{
		"duplicate id":       {"id": ids[0], "used": "no", "helpful": "no"},
		"bad used":           {"id": ids[1], "used": "maybe", "helpful": "yes"},
		"bad helpful":        {"id": ids[1], "used": "no", "helpful": "maybe"},
		"how over 200 runes": {"id": ids[1], "used": "yes", "helpful": "yes", "how": strings.Repeat("h", 201)},
	}
	for name, item := range bad {
		args := feedbackItems(valid, item)
		args["ask"] = askID
		if _, isErr := call(t, cs, "feedback", args); !isErr {
			t.Fatalf("%s: refused item accepted", name)
		}
		for _, o := range outcomes(t, home) {
			if o.Resolved.Valid || o.Label.Valid {
				t.Fatalf("%s: row written by a refused call: %+v", name, o)
			}
		}
	}
	args := feedbackItems(valid)
	args["ask"] = askID
	if _, isErr := call(t, cs, "feedback", args); isErr {
		t.Fatal("ask was answered by a refused call")
	}
}

func TestMCPFeedbackForgottenMemoryWritesNothing(t *testing.T) {
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	cs := connect(t, h, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	if out, isErr := call(t, cs, "forget", map[string]any{"id": ids[0]}); isErr {
		t.Fatalf("forget = %q", out)
	}
	args := feedbackItems(
		map[string]any{"id": ids[1], "used": "no", "helpful": "wrong"},
		map[string]any{"id": ids[0], "used": "no", "helpful": "wrong"},
	)
	args["ask"] = askID
	if _, isErr := call(t, cs, "feedback", args); !isErr {
		t.Fatal("answer about a forgotten memory accepted")
	}
	for _, o := range outcomes(t, home) {
		if o.Resolved.Valid || o.Label.Valid {
			t.Fatalf("row written by a refused call: %+v", o)
		}
	}
	mems, err := h.List(context.Background(), inscribe.ListFilter{Project: proj})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mems {
		if m.ID == ids[1] && m.Penalty != 0 {
			t.Fatalf("penalty %v added by a refused call", m.Penalty)
		}
	}
}

func TestFeedbackEmptyProjectRefused(t *testing.T) {
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	_, _, err := h.Feedback(context.Background(), "", askID, []feedback.Answer{{ID: ids[0], Used: "no", Helpful: "wrong"}})
	if err == nil {
		t.Fatal("ask accepted for an empty project")
	}
	for _, o := range outcomes(t, home) {
		if o.Resolved.Valid || o.Label.Valid {
			t.Fatalf("row written by a refused call: %+v", o)
		}
	}
}

func TestMCPFeedbackDebugLogsLine(t *testing.T) {
	t.Setenv("INSCRIBE_DEBUG", "1")
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	cs := connect(t, h, proj)
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	askID := askRound(t, h, proj, "s1", "d1", time.Now())
	args := feedbackItems(map[string]any{"id": ids[0], "used": "no", "helpful": "wrong"})
	args["ask"] = askID
	if out, isErr := call(t, cs, "feedback", args); isErr {
		t.Fatalf("feedback = %q", out)
	}
	logged, err := os.ReadFile(filepath.Join(home, "inscribe.log"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "feedback " + askID + ": labeled 1 rows, 1 wrong"; !strings.Contains(string(logged), want) {
		t.Fatalf("inscribe.log = %q, want a line %q", logged, want)
	}
}

func TestMCPFeedbackRepeatedWrongAcrossAsks(t *testing.T) {
	proj := t.TempDir()
	home := feedbackHome(t)
	h := openAt(t, home, proj)
	cs := connect(t, h, proj)
	ctx := context.Background()
	ids := rememberAll(t, h, proj, "deploy make target alpha", "deploy make target bravo", "deploy make target charlie", "deploy make target delta")
	global, err := h.Remember(ctx, "deploy make target is the global rule", model.Note, model.Global, "")
	if err != nil {
		t.Fatal(err)
	}
	// Each wrong answer lowers the memory's recall score, so it stops being
	// injected after a few; the asks are made first and answered after.
	now := time.Now()
	var asks []string
	for i := range 5 {
		asks = append(asks, askRound(t, h, proj, "s1", fmt.Sprintf("d%d", i), now.Add(time.Duration(i)*11*time.Minute)))
	}
	for i, askID := range asks {
		args := feedbackItems(
			map[string]any{"id": ids[0], "used": "no", "helpful": "wrong"},
			map[string]any{"id": global, "used": "no", "helpful": "wrong"},
		)
		args["ask"] = askID
		if out, isErr := call(t, cs, "feedback", args); isErr {
			t.Fatalf("answer %d refused: %s", i, out)
		}
	}
	mems, err := h.List(ctx, inscribe.ListFilter{Project: proj})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]model.Memory{}
	for _, m := range mems {
		found[m.ID] = m
	}
	project, ok := found[ids[0]]
	if !ok || math.Abs(project.Penalty-1.0) > 1e-9 || project.EffectiveConfidence() != 0 {
		t.Fatalf("project memory = %+v, want penalty 1.0 and confidence floored at 0", project)
	}
	gm, ok := found[global]
	if !ok || math.Abs(gm.Penalty-0.25) > 1e-9 {
		t.Fatalf("global memory penalty = %v, want 0.25 (0.05 per wrong answer)", gm.Penalty)
	}
}

package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func openInscribe(t *testing.T, proj string) *inscribe.Inscribe {
	t.Helper()
	h, err := inscribe.Open(inscribe.Options{Home: t.TempDir(), Caller: "test", Project: proj})
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

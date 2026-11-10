package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func openHive(t *testing.T, proj string) *hive.Hive {
	t.Helper()
	h, err := hive.Open(hive.Options{Home: t.TempDir(), Caller: "test", Project: proj})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func connect(t *testing.T, h *hive.Hive, project string) *mcp.ClientSession {
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
	cs := connect(t, openHive(t, proj), proj)
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
	h := openHive(t, proj)
	cs := connect(t, h, proj)
	id, _ := call(t, cs, "remember", map[string]any{"text": "deploys go through make deploy", "type": "note", "scope": "project", "project": proj})
	status, isErr := call(t, cs, "forget", map[string]any{"id": strings.TrimSpace(id)})
	if isErr || !strings.Contains(status, "forgotten") {
		t.Fatalf("forget = %q, error %v", status, isErr)
	}
	left, err := h.List(context.Background(), hive.ListFilter{Project: proj, Statuses: []model.Status{model.Forgotten}})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].ID != strings.TrimSpace(id) {
		t.Fatalf("forgotten memory was purged: %v", left)
	}
}

func TestMCPBadArgs(t *testing.T) {
	cs := connect(t, openHive(t, t.TempDir()), t.TempDir())
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
	cs := connect(t, openHive(t, proj), proj)
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
	h := openHive(t, projA)
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
	h := openHive(t, projA)
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
	h := openHive(t, projA)
	cs := connect(t, h, projA)
	if _, isErr := call(t, cs, "remember", map[string]any{"text": "deploy with make deploy"}); isErr {
		t.Fatal("remember failed")
	}
	ms, err := h.List(context.Background(), hive.ListFilter{Project: projA})
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
	cs := connect(t, openHive(t, proj), proj)
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

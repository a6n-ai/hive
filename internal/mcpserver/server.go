// Package mcpserver serves Inscribe's memory over the Model Context Protocol.
// Tools return errors as tool results, never panics, and cannot purge.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/capture"
	"github.com/a6n-ai/inscribe/internal/feedback"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxText    = 2000
	maxBudget  = 2000
	maxProject = 4096
	maxQuery   = 2000
	// maxEvidence caps one why line so a long commit heredoc stays a glance.
	maxEvidence = 240
)

type rememberIn struct {
	Text    string `json:"text" jsonschema:"the memory to keep, at most 2000 characters"`
	Type    string `json:"type,omitempty" jsonschema:"note, preference or decision (default note)"`
	Scope   string `json:"scope,omitempty" jsonschema:"project or global (default project)"`
	Project string `json:"project,omitempty" jsonschema:"optional; a directory inside this session's folder, resolved to its git root. Omit to use the session project. Recall always adds global memories"`
}

type recallIn struct {
	Query   string `json:"query,omitempty" jsonschema:"text to match; empty recalls session-start memories"`
	Project string `json:"project,omitempty" jsonschema:"optional; a directory inside this session's folder, resolved to its git root. Omit to use the session project. Recall always adds global memories"`
	Budget  int    `json:"budget,omitempty" jsonschema:"token budget, 0 to 2000 (0 uses the configured default)"`
}

type idIn struct {
	ID string `json:"id" jsonschema:"memory id"`
}

type feedbackIn struct {
	Ask   string            `json:"ask" jsonschema:"the ask id from the feedback question"`
	Items []feedback.Answer `json:"items" jsonschema:"one answer per memory id the question lists"`
}

// New returns a server with the remember, recall, forget, why and feedback
// tools. Calls act on project only; memories of other projects are invisible to them.
func New(h *inscribe.Inscribe, project string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "inscribe", Version: "dev"}, nil)
	no := false
	// Annotations let Claude Code auto mode run these tools without a prompt:
	// all stay inside the local store, none erases data (forget keeps it).
	write := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}
	// A second answer to the same ask is refused, so this is not idempotent.
	answer := &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}
	mcp.AddTool(s, &mcp.Tool{Name: "remember", Description: "Store a memory for this project or globally.", Annotations: write}, rememberTool(h, project))
	mcp.AddTool(s, &mcp.Tool{Name: "recall", Description: "Recall memories relevant to a query or the session start.", Annotations: read}, recallTool(h, project))
	mcp.AddTool(s, &mcp.Tool{Name: "forget", Description: "Mark a memory forgotten. Data is kept.", Annotations: write}, forgetTool(h, project))
	mcp.AddTool(s, &mcp.Tool{Name: "why", Description: "Show the events that evidence a memory.", Annotations: read}, whyTool(h, project))
	mcp.AddTool(s, &mcp.Tool{Name: "feedback", Description: "Answer an inscribe feedback question: for each memory it lists, whether you used it, whether it helped, and why.", Annotations: answer}, feedbackTool(h, project))
	return s
}

// scopedProject returns the project a call acts on: the server's, or the git
// root of a directory inside it, so a session opened in a workspace folder can
// reach the repositories under it. Sibling and parent projects are refused.
func scopedProject(arg, project string) (string, error) {
	if len(arg) > maxProject {
		return "", fmt.Errorf("project is over %d characters", maxProject)
	}
	if arg == "" || arg == project {
		return project, nil
	}
	// The server's cwd is not the session's, so relative paths start at the project.
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(project, arg)
	}
	root, got := capture.ResolveProject(project), capture.ResolveProject(arg)
	if got == root {
		return project, nil
	}
	if strings.HasPrefix(got, root+string(filepath.Separator)) {
		return got, nil
	}
	return "", fmt.Errorf("project %q is not inside this server's project %q", arg, project)
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func rememberTool(h *inscribe.Inscribe, project string) mcp.ToolHandlerFor[rememberIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in rememberIn) (*mcp.CallToolResult, any, error) {
		if in.Text == "" {
			return nil, nil, errors.New("text is required")
		}
		if utf8.RuneCountInString(in.Text) > maxText {
			return nil, nil, fmt.Errorf("text is over %d characters", maxText)
		}
		proj, err := scopedProject(in.Project, project)
		if err != nil {
			return nil, nil, err
		}
		in.Project = proj
		t, scope, err := memoryKind(in)
		if err != nil {
			return nil, nil, err
		}
		id, err := h.Remember(ctx, in.Text, t, scope, in.Project)
		if err != nil {
			return nil, nil, err
		}
		return textResult(id), nil, nil
	}
}

// memoryKind checks the type and scope arguments and applies their defaults.
func memoryKind(in rememberIn) (inscribe.MemoryType, inscribe.ScopeKind, error) {
	t := model.MemoryType(in.Type)
	if in.Type == "" {
		t = model.Note
	}
	switch t {
	case model.Note, model.Preference, model.Decision:
	default:
		return "", "", fmt.Errorf("unknown type %q", in.Type)
	}
	scope := model.ScopeKind(in.Scope)
	if in.Scope == "" {
		scope = model.Global
		if in.Project != "" {
			scope = model.ProjectScope
		}
	}
	switch scope {
	case model.ProjectScope:
		if in.Project == "" {
			return "", "", errors.New("project scope needs a project")
		}
	case model.Global:
	default:
		return "", "", fmt.Errorf("unknown scope %q", in.Scope)
	}
	return t, scope, nil
}

func recallTool(h *inscribe.Inscribe, project string) mcp.ToolHandlerFor[recallIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in recallIn) (*mcp.CallToolResult, any, error) {
		if in.Budget < 0 || in.Budget > maxBudget {
			return nil, nil, fmt.Errorf("budget must be 0 to %d", maxBudget)
		}
		if utf8.RuneCountInString(in.Query) > maxQuery {
			return nil, nil, fmt.Errorf("query is over %d characters", maxQuery)
		}
		proj, err := scopedProject(in.Project, project)
		if err != nil {
			return nil, nil, err
		}
		q := inscribe.Query{Project: proj, Text: in.Query, Budget: in.Budget, Mode: recall.SessionStart}
		if in.Query != "" {
			q.Mode = recall.Prompt
		}
		lines, err := h.Recall(ctx, q)
		if err != nil {
			return nil, nil, err
		}
		return textResult(recall.Render(lines)), nil, nil
	}
}

func forgetTool(h *inscribe.Inscribe, project string) mcp.ToolHandlerFor[idIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		if err := requireMemory(ctx, h, project, in.ID); err != nil {
			return nil, nil, err
		}
		// Purge is never offered over MCP: an agent must not erase data for good.
		if err := h.Forget(ctx, in.ID, false); err != nil {
			return nil, nil, err
		}
		return textResult("status: forgotten " + in.ID), nil, nil
	}
}

func whyTool(h *inscribe.Inscribe, project string) mcp.ToolHandlerFor[idIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		if err := requireMemory(ctx, h, project, in.ID); err != nil {
			return nil, nil, err
		}
		evs, err := h.Why(ctx, in.ID)
		if err != nil {
			return nil, nil, err
		}
		var b strings.Builder
		for _, e := range evs {
			b.WriteString(evidenceLine(e))
			b.WriteByte('\n')
		}
		return textResult(b.String()), nil, nil
	}
}

func feedbackTool(h *inscribe.Inscribe, project string) mcp.ToolHandlerFor[feedbackIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in feedbackIn) (*mcp.CallToolResult, any, error) {
		labeled, wrong, err := h.Feedback(ctx, project, in.Ask, in.Items)
		if err != nil {
			return nil, nil, err
		}
		return textResult(fmt.Sprintf("labeled %d rows, %d wrong", labeled, wrong)), nil, nil
	}
}

// evidenceLine renders one event for an agent reading why over MCP: enough
// to judge whether the memory is still true, without flooding its context.
// Events carry PromptText (prompts) or Payload (tool calls: command,
// exit_code, file paths, ...). Payloads are already secret-scrubbed.
func evidenceLine(e model.Event) string {
	parts := []string{"[" + string(e.Kind) + "]"}
	if e.Tool != "" {
		parts = append(parts, e.Tool)
	}
	head := len(parts)
	if e.PromptText != "" {
		parts = append(parts, e.PromptText)
	}
	for _, k := range []string{"text", "command", "path", "skill"} {
		if s, ok := e.Payload[k].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	if c, ok := e.Payload["exit_code"]; ok && c != nil {
		parts = append(parts, fmt.Sprintf("(exit %v)", c))
	}
	if s, ok := e.Payload["error"].(string); ok && s != "" {
		parts = append(parts, "error: "+s)
	}
	// Unknown event shapes still show their payload rather than nothing.
	if len(parts) == head && len(e.Payload) > 0 {
		b, _ := json.Marshal(e.Payload)
		parts = append(parts, string(b))
	}
	line := strings.Join(parts, " ")
	if r := []rune(line); len(r) > maxEvidence {
		line = string(r[:maxEvidence]) + "…"
	}
	return line
}

// requireMemory fails unless a memory with id exists in any status and is
// visible to project: global, the project's own, or scoped to a repository or
// file inside it.
func requireMemory(ctx context.Context, h *inscribe.Inscribe, project, id string) error {
	if id == "" {
		return errors.New("id is required")
	}
	all := []model.Status{model.Active, model.Candidate, model.Superseded, model.Stale, model.Forgotten}
	// shortcut: scans every memory to find one id; add an ID filter to ListFilter if the store grows large.
	ms, err := h.List(ctx, inscribe.ListFilter{Statuses: all})
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.ID == id && visible(m, project) {
			return nil
		}
	}
	return fmt.Errorf("no memory %q", id)
}

func visible(m model.Memory, project string) bool {
	return m.ScopeKind == model.Global || m.Scope == project ||
		strings.HasPrefix(m.Scope, project+string(filepath.Separator))
}

// Package mcpserver serves Hive's memory over the Model Context Protocol.
// Tools return errors as tool results, never panics, and cannot purge.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxText    = 2000
	maxBudget  = 2000
	maxProject = 4096
	maxQuery   = 2000
)

type rememberIn struct {
	Text    string `json:"text" jsonschema:"the memory to keep, at most 2000 characters"`
	Type    string `json:"type,omitempty" jsonschema:"note, preference or decision (default note)"`
	Scope   string `json:"scope,omitempty" jsonschema:"project or global (default project)"`
	Project string `json:"project,omitempty" jsonschema:"project directory; only this server's project is allowed"`
}

type recallIn struct {
	Query   string `json:"query,omitempty" jsonschema:"text to match; empty recalls session-start memories"`
	Project string `json:"project,omitempty" jsonschema:"project directory; only this server's project is allowed"`
	Budget  int    `json:"budget,omitempty" jsonschema:"token budget, 0 to 2000 (0 uses the configured default)"`
}

type idIn struct {
	ID string `json:"id" jsonschema:"memory id"`
}

// New returns a server with the remember, recall, forget and why tools. Calls
// act on project only; memories of other projects are invisible to them.
func New(h *hive.Hive, project string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "hive", Version: "dev"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "remember", Description: "Store a memory for this project or globally."}, rememberTool(h, project))
	mcp.AddTool(s, &mcp.Tool{Name: "recall", Description: "Recall memories relevant to a query or the session start."}, recallTool(h, project))
	mcp.AddTool(s, &mcp.Tool{Name: "forget", Description: "Mark a memory forgotten. Data is kept."}, forgetTool(h, project))
	mcp.AddTool(s, &mcp.Tool{Name: "why", Description: "Show the events that evidence a memory."}, whyTool(h, project))
	return s
}

// scopedProject returns the project a call acts on: the server's, unless the
// call names another project, which is refused.
func scopedProject(arg, project string) (string, error) {
	if len(arg) > maxProject {
		return "", fmt.Errorf("project is over %d characters", maxProject)
	}
	if arg != "" && arg != project {
		return "", fmt.Errorf("project %q is not this server's project", arg)
	}
	return project, nil
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func rememberTool(h *hive.Hive, project string) mcp.ToolHandlerFor[rememberIn, any] {
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
func memoryKind(in rememberIn) (hive.MemoryType, hive.ScopeKind, error) {
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

func recallTool(h *hive.Hive, project string) mcp.ToolHandlerFor[recallIn, any] {
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
		q := hive.Query{Project: proj, Text: in.Query, Budget: in.Budget, Mode: recall.SessionStart}
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

func forgetTool(h *hive.Hive, project string) mcp.ToolHandlerFor[idIn, any] {
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

func whyTool(h *hive.Hive, project string) mcp.ToolHandlerFor[idIn, any] {
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
			fmt.Fprintf(&b, "%s %s\n", e.Kind, e.Tool)
		}
		return textResult(b.String()), nil, nil
	}
}

// requireMemory fails unless a memory with id exists in any status and is
// visible to project (its own, global, or a file inside it).
func requireMemory(ctx context.Context, h *hive.Hive, project, id string) error {
	if id == "" {
		return errors.New("id is required")
	}
	all := []model.Status{model.Active, model.Candidate, model.Superseded, model.Stale, model.Forgotten}
	ms, err := h.List(ctx, hive.ListFilter{Statuses: all, Project: project})
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.ID == id {
			return nil
		}
	}
	return fmt.Errorf("no memory %q", id)
}

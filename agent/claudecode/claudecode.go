// Package claudecode adapts Claude Code hook payloads to Hive events.
package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/capture"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
)

const name = "claude-code"

var eventKinds = map[string]model.EventKind{
	"SessionStart":       model.SessionStart,
	"UserPromptSubmit":   model.Prompt,
	"PostToolUse":        model.PostTool,
	"PostToolUseFailure": model.PostTool,
	"Stop":               model.TurnEnd,
	"SessionEnd":         model.SessionEnd,
}

// toolFields maps Claude Code tool names to Hive's canonical tool name and the
// tool_input field kept in the payload under key.
var toolFields = map[string]struct{ tool, input, key string }{
	"Read":         {"read", "file_path", "path"},
	"Edit":         {"edit", "file_path", "path"},
	"MultiEdit":    {"edit", "file_path", "path"},
	"Write":        {"write", "file_path", "path"},
	"NotebookEdit": {"edit", "notebook_path", "path"},
	"Skill":        {"skill", "skill", "skill"},
}

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func init() { agent.Register(New()) }

type hookPayload struct {
	SessionID string         `json:"session_id"`
	Cwd       string         `json:"cwd"`
	Prompt    string         `json:"prompt"`
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Error     string         `json:"error"`
}

func (a *Adapter) Name() string { return name }

func (a *Adapter) Parse(event string, stdin []byte) (model.Event, error) {
	kind, ok := eventKinds[event]
	if !ok {
		return model.Event{}, fmt.Errorf("claude-code: unknown event %q", event)
	}
	var p hookPayload
	if err := json.Unmarshal(stdin, &p); err != nil {
		return model.Event{}, fmt.Errorf("claude-code: %s payload: %w", event, err)
	}
	if p.SessionID == "" {
		return model.Event{}, errors.New("claude-code: payload has no session_id")
	}
	e := model.Event{Agent: name, Session: p.SessionID, Project: capture.ResolveProject(p.Cwd), Kind: kind, Payload: map[string]any{}}
	switch kind {
	case model.Prompt:
		e.PromptText = p.Prompt
	case model.PostTool:
		e.Tool, e.Payload = normalizeTool(event, p)
	}
	return e, nil
}

func normalizeTool(event string, p hookPayload) (string, map[string]any) {
	if p.ToolName == "Bash" {
		return "shell", shellPayload(event, p)
	}
	out := map[string]any{}
	tool := p.ToolName
	if f, ok := toolFields[p.ToolName]; ok {
		tool = f.tool
		out[f.key], _ = p.ToolInput[f.input].(string)
	}
	if event == "PostToolUseFailure" {
		out["error"] = p.Error
	}
	return tool, out
}

// shellPayload reads the exit code from a failure's "Exit code N" first line.
// shortcut: a failure without that line keeps the whole text as error and no exit code.
func shellPayload(event string, p hookPayload) map[string]any {
	cmd, _ := p.ToolInput["command"].(string)
	out := map[string]any{"command": cmd}
	if event != "PostToolUseFailure" {
		out["exit_code"] = 0
		return out
	}
	head, rest, _ := strings.Cut(p.Error, "\n")
	if code, ok := strings.CutPrefix(head, "Exit code "); ok {
		if n, err := strconv.Atoi(code); err == nil {
			out["exit_code"] = n
			out["error"] = rest
			return out
		}
	}
	out["error"] = p.Error
	return out
}

func (a *Adapter) Reply(event string, lines []recall.Line) []byte {
	switch event {
	case "SessionStart", "UserPromptSubmit", "PostToolUse", "PostToolUseFailure":
	default:
		return nil
	}
	if len(lines) == 0 {
		return nil
	}
	out, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":     event,
		"additionalContext": recall.Render(lines),
	}})
	if err != nil {
		return nil
	}
	return out
}

func (a *Adapter) Capabilities() agent.Caps {
	return agent.Caps{CanInject: true, CanBlock: true, HasPreTool: true, HasSessionEnd: true}
}

func (a *Adapter) InstructionsFile() string { return "CLAUDE.md" }

func (a *Adapter) SkillDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".claude", "plugins", "cache")}
}

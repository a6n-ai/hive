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
		e.Tool, e.Payload = NormalizeTool(p.ToolName, p.ToolInput, event == "PostToolUseFailure", p.Error)
	}
	return e, nil
}

// NormalizeTool maps a Claude Code tool call to Hive's canonical tool name and
// payload. errText is the failure text when failed is true.
func NormalizeTool(name string, input map[string]any, failed bool, errText string) (string, map[string]any) {
	if name == "Bash" {
		return "shell", shellPayload(input, failed, errText)
	}
	out := map[string]any{}
	tool := name
	if f, ok := toolFields[name]; ok {
		tool = f.tool
		out[f.key], _ = input[f.input].(string)
	}
	if failed {
		out["error"] = errText
	}
	return tool, out
}

// shellPayload reads the exit code from a failure's "Exit code N" first line.
// shortcut: a failure without that line keeps the whole text as error and no exit code.
func shellPayload(input map[string]any, failed bool, errText string) map[string]any {
	cmd, _ := input["command"].(string)
	out := map[string]any{"command": cmd}
	if !failed {
		out["exit_code"] = 0
		return out
	}
	head, rest, _ := strings.Cut(errText, "\n")
	if code, ok := strings.CutPrefix(head, "Exit code "); ok {
		if n, err := strconv.Atoi(code); err == nil {
			out["exit_code"] = n
			out["error"] = rest
			return out
		}
	}
	out["error"] = errText
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

// Headless runs Claude Code in print mode with the given model and no tools, so
// text from transcripts cannot make it act. It keeps its normal auth (no --bare).
func (a *Adapter) Headless(model string) agent.HeadlessSpec {
	return agent.HeadlessSpec{
		Command: []string{"claude", "-p", "--output-format", "json", "--model", model,
			"--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--permission-mode", "dontAsk"},
		ParseOutput: parseHeadless,
	}
}

func parseHeadless(out []byte) (string, error) {
	var r struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return "", fmt.Errorf("claude-code: headless output: %w", err)
	}
	if r.IsError {
		return "", errors.New("claude-code: headless run reported an error")
	}
	if r.Result == "" {
		return "", errors.New("claude-code: headless output has no result")
	}
	return r.Result, nil
}

func (a *Adapter) SkillDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".claude", "plugins", "cache")}
}

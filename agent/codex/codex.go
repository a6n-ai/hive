// Package codex adapts Codex CLI hook payloads to Inscribe events.
package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/a6n-ai/inscribe/agent"
	"github.com/a6n-ai/inscribe/internal/capture"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/model"
)

const name = "codex"

var eventKinds = map[string]model.EventKind{
	"SessionStart":     model.SessionStart,
	"UserPromptSubmit": model.Prompt,
	"PostToolUse":      model.PostTool,
	"Stop":             model.TurnEnd,
	"SessionEnd":       model.SessionEnd,
}

var shellTools = map[string]bool{"bash": true, "shell": true, "exec_command": true, "local_shell": true,
	"unified_exec": true, "container.exec": true, "exec": true}

var patchFile = regexp.MustCompile(`^\*\*\* (?:Update|Add|Delete) File:\s*(.+?)\s*$`)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func init() { agent.Register(New()) }

type hookPayload struct {
	SessionID    string         `json:"session_id"`
	Cwd          string         `json:"cwd"`
	Prompt       string         `json:"prompt"`
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse any            `json:"tool_response"`
}

func (a *Adapter) Name() string { return name }

func (a *Adapter) Parse(event string, stdin []byte) (model.Event, error) {
	kind, ok := eventKinds[event]
	if !ok {
		return model.Event{}, fmt.Errorf("codex: unknown event %q", event)
	}
	var p hookPayload
	if err := json.Unmarshal(stdin, &p); err != nil {
		return model.Event{}, fmt.Errorf("codex: %s payload: %w", event, err)
	}
	if p.SessionID == "" {
		return model.Event{}, errors.New("codex: payload has no session_id")
	}
	e := model.Event{Agent: name, Session: p.SessionID, Project: capture.ResolveProject(p.Cwd), Cwd: p.Cwd, Kind: kind, Payload: map[string]any{}}
	switch kind {
	case model.Prompt:
		e.PromptText = p.Prompt
	case model.PostTool:
		e.Tool, e.Payload = normalize(p.ToolName, p.ToolInput, p.ToolResponse)
	}
	return e, nil
}

// normalize maps a Codex tool call to Inscribe's canonical tool name and payload.
// A non-zero exit code makes the response text the error.
func normalize(tool string, input map[string]any, resp any) (string, map[string]any) {
	out := map[string]any{}
	switch {
	case shellTools[tool]:
		tool = "shell"
		out["command"] = command(input)
	case tool == "apply_patch":
		tool = "edit"
		paths := patchPaths(patchText(input))
		if len(paths) > 0 {
			out["path"] = paths[0]
		}
		out["paths"] = paths
	}
	code, hasCode := agent.ExitCode(resp)
	if hasCode {
		out["exit_code"] = code
		if code != 0 {
			out["error"] = agent.ResponseText(resp)
		}
	}
	return tool, out
}

func command(input map[string]any) string {
	for _, k := range []string{"command", "cmd"} {
		switch v := input[k].(type) {
		case string:
			return v
		case []any:
			return joinArgs(v)
		}
	}
	return ""
}

func joinArgs(args []any) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		s, _ := a.(string)
		parts = append(parts, agent.ShellQuote(s))
	}
	return strings.Join(parts, " ")
}

func patchText(input map[string]any) string {
	for _, k := range []string{"input", "patch", "command"} {
		if s, ok := input[k].(string); ok {
			return s
		}
	}
	return ""
}

func patchPaths(text string) []string {
	var paths []string
	for _, line := range strings.Split(text, "\n") {
		if m := patchFile.FindStringSubmatch(line); m != nil {
			paths = append(paths, m[1])
		}
	}
	return paths
}

func (a *Adapter) Reply(event string, lines []recall.Line) []byte {
	switch event {
	case "SessionStart", "UserPromptSubmit", "PostToolUse":
		return agent.ContextJSON(event, lines)
	}
	return nil
}

func (a *Adapter) Capabilities() agent.Caps {
	return agent.Caps{CanInject: true, CanBlock: true, HasPreTool: true, HasSessionEnd: true}
}

func (a *Adapter) InstructionsFile() string { return "AGENTS.md" }

func (a *Adapter) SkillDirs() []string { return []string{filepath.Join(codexHome(), "skills")} }

// Headless is empty: there is no verified way to run Codex with tools off, so
// consolidation does not run on Codex.
func (a *Adapter) Headless(string) agent.HeadlessSpec { return agent.HeadlessSpec{} }

// Package gemini adapts Gemini CLI hook payloads to Hive events. It is written
// from the Gemini CLI documentation and has not been checked against a live CLI.
package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/capture"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
)

const name = "gemini"

var eventKinds = map[string]model.EventKind{
	"SessionStart": model.SessionStart,
	"BeforeAgent":  model.Prompt,
	"AfterTool":    model.PostTool,
	"AfterAgent":   model.TurnEnd,
	"SessionEnd":   model.SessionEnd,
}

var exitLine = regexp.MustCompile(`(?m)^Exit Code: (-?\d+)[ \t]*(?:\n|$)`)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func init() { agent.Register(New()) }

// hookPayload fields are untyped where the docs leave the shape open, so a
// payload with another shape is read as far as it goes rather than rejected.
type hookPayload struct {
	SessionID  string `json:"session_id"`
	Cwd        string `json:"cwd"`
	Prompt     string `json:"prompt"`
	UserPrompt string `json:"user_prompt"`
	LLMRequest any    `json:"llm_request"`
	ToolName   string `json:"tool_name"`
	ToolInput  any    `json:"tool_input"`
	ToolArgs   any    `json:"tool_args"`
	ToolResp   any    `json:"tool_response"`
	ToolResult any    `json:"tool_result"`
}

func (a *Adapter) Name() string { return name }

func (a *Adapter) Parse(event string, stdin []byte) (model.Event, error) {
	kind, ok := eventKinds[event]
	if !ok {
		return model.Event{}, fmt.Errorf("gemini: unknown event %q", event)
	}
	var p hookPayload
	if err := json.Unmarshal(stdin, &p); err != nil {
		return model.Event{}, fmt.Errorf("gemini: %s payload: %w", event, err)
	}
	if p.SessionID == "" {
		return model.Event{}, errors.New("gemini: payload has no session_id")
	}
	e := model.Event{Agent: name, Session: p.SessionID, Project: capture.ResolveProject(p.Cwd), Kind: kind, Payload: map[string]any{}}
	switch kind {
	case model.Prompt:
		e.PromptText = p.promptText()
	case model.PostTool:
		e.Tool, e.Payload = normalize(p.ToolName, orMap(p.ToolInput, p.ToolArgs), p.response())
	}
	return e, nil
}

func (p hookPayload) promptText() string {
	switch {
	case p.Prompt != "":
		return p.Prompt
	case p.UserPrompt != "":
		return p.UserPrompt
	}
	s, _ := p.LLMRequest.(string)
	return s
}

// response is the tool response that carries a structured exit code if either
// field does, so text can never override a code the agent reported.
func (p hookPayload) response() any {
	for _, r := range []any{p.ToolResp, p.ToolResult} {
		if _, ok := agent.ExitCode(r); ok {
			return r
		}
	}
	return orAny(p.ToolResp, p.ToolResult)
}

func orAny(a, b any) any {
	if a != nil {
		return a
	}
	return b
}

func orMap(a, b any) map[string]any {
	if m, ok := a.(map[string]any); ok {
		return m
	}
	m, _ := b.(map[string]any)
	return m
}

// normalize maps a Gemini CLI tool call to Hive's canonical tool name and payload.
func normalize(tool string, input map[string]any, resp any) (string, map[string]any) {
	out := map[string]any{}
	switch tool {
	case "run_shell_command":
		tool = "shell"
		out["command"], _ = input["command"].(string)
		shellResult(out, resp)
	case "read_file", "read_many_files":
		tool = "read"
		out["path"] = pathOf(input)
	case "write_file":
		tool = "write"
		out["path"] = pathOf(input)
	case "replace", "edit":
		tool = "edit"
		out["path"] = pathOf(input)
	case "activate_skill":
		tool = "skill"
		out["skill"], _ = input["name"].(string)
	}
	return tool, out
}

// shellResult takes the exit code from the response keys, or else from the
// last "Exit Code: N" line in the text, so output that echoes an earlier marker
// cannot spoof it. A non-zero code makes the text before that line the error.
func shellResult(out map[string]any, resp any) {
	if code, ok := agent.ExitCode(resp); ok {
		out["exit_code"] = code
		if code != 0 {
			out["error"] = agent.ResponseText(resp)
		}
		return
	}
	text := agent.ResponseText(resp)
	all := exitLine.FindAllStringSubmatchIndex(text, -1)
	if len(all) == 0 {
		return
	}
	last := all[len(all)-1]
	var code int
	_, _ = fmt.Sscanf(text[last[2]:last[3]], "%d", &code)
	out["exit_code"] = code
	if code != 0 {
		out["error"] = strings.TrimSpace(text[:last[0]])
	}
}

func pathOf(input map[string]any) string {
	for _, k := range []string{"file_path", "absolute_path"} {
		if s, ok := input[k].(string); ok {
			return s
		}
	}
	return ""
}

func (a *Adapter) Reply(event string, lines []recall.Line) []byte {
	switch event {
	case "SessionStart", "BeforeAgent", "AfterTool":
		return agent.ContextJSON(event, lines)
	}
	return nil
}

func (a *Adapter) Capabilities() agent.Caps {
	return agent.Caps{CanInject: true, CanBlock: false, HasPreTool: false, HasSessionEnd: true}
}

func (a *Adapter) InstructionsFile() string { return "GEMINI.md" }

func (a *Adapter) SkillDirs() []string { return []string{agent.UserPath(".gemini", "skills")} }

// Headless is empty: there is no verified way to run Gemini CLI with tools off.
func (a *Adapter) Headless(string) agent.HeadlessSpec { return agent.HeadlessSpec{} }

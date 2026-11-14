// Package importer backfills Inscribe's events from existing agent transcripts.
package importer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe/agent/claudecode"
	"github.com/a6n-ai/inscribe/internal/capture"
	"github.com/a6n-ai/inscribe/internal/consolidate"
	"github.com/a6n-ai/inscribe/model"
)

const agentName = "claude-code"

// Options selects the transcripts to import. Root defaults to ~/.claude/projects.
type Options struct {
	Root    string
	Since   time.Time
	Project string
}

// Summary counts what an import read and produced. Skipped counts lines that
// were malformed, sidechain, or of an unknown type.
type Summary struct {
	Files, Lines, Skipped, Events int
}

// RawLine is one decoded Claude Code transcript line.
type RawLine struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	SessionID   string `json:"sessionId"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *Usage          `json:"usage"`
	} `json:"message"`
}

// Usage is the token usage Claude Code records on an assistant message.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// lineCtx is one accepted transcript line with its resolved time and project.
type lineCtx struct {
	l       RawLine
	ts      int64
	project string
}

type toolUse struct {
	name  string
	input map[string]any
}

// fileImport holds the state of one transcript file.
type fileImport struct {
	opts     Options
	sink     func(context.Context, model.Event) error
	sum      *Summary
	started  map[string]bool
	prompted map[string]bool
	pending  map[string]toolUse
	projects map[string]string
	// promptChecked is set at the transcript's first prompt; skip drops the file
	// when that prompt is a consolidation review.
	promptChecked, skip bool
}

// ClaudeCode reads Claude Code transcripts under opts.Root and hands each
// derived event to sink. Events whose digest was already stored are rejected by
// the sink, so a repeat import adds nothing.
func ClaudeCode(ctx context.Context, opts Options, sink func(context.Context, model.Event) error) (Summary, error) {
	root := opts.Root
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Summary{}, err
		}
		root = filepath.Join(home, ".claude", "projects")
	}
	if opts.Project != "" {
		opts.Project = capture.ResolveProject(opts.Project)
	}
	files, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return Summary{}, err
	}
	var sum Summary
	for _, path := range files {
		if err := importFile(ctx, path, opts, sink, &sum); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

func importFile(ctx context.Context, path string, opts Options, sink func(context.Context, model.Event) error, sum *Summary) error {
	imp := &fileImport{opts: opts, sink: sink, sum: sum, started: map[string]bool{},
		prompted: map[string]bool{}, pending: map[string]toolUse{}, projects: map[string]string{}}
	sum.Files++
	lines, malformed, err := ReadTranscript(path, func(l RawLine) error { return imp.line(ctx, l) })
	sum.Lines += lines
	sum.Skipped += malformed
	return err
}

// ReadTranscript decodes every line of a transcript and passes it to fn, with no
// filtering. Lines that are not valid JSON are counted as malformed and skipped.
func ReadTranscript(path string, fn func(RawLine) error) (lines, malformed int, err error) {
	f, err := os.Open(path) //nolint:gosec // G304: transcripts come from the user's Claude Code projects directory
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		raw, rerr := r.ReadBytes('\n')
		if len(raw) > 0 {
			lines++
			var l RawLine
			if json.Unmarshal(bytes.TrimSpace(raw), &l) != nil {
				malformed++
			} else if ferr := fn(l); ferr != nil {
				return lines, malformed, ferr
			}
		}
		if errors.Is(rerr, io.EOF) {
			return lines, malformed, nil
		}
		if rerr != nil {
			return lines, malformed, rerr
		}
	}
}

// line dispatches one decoded transcript line. Lines that lack a session or
// time, are sidechains, or have an unknown type are skipped.
func (f *fileImport) line(ctx context.Context, l RawLine) error {
	if l.SessionID == "" || l.Timestamp == "" {
		f.sum.Skipped++
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		f.sum.Skipped++
		return nil
	}
	if !f.opts.Since.IsZero() && t.Before(f.opts.Since) {
		return nil
	}
	lc := lineCtx{l: l, ts: t.UnixMilli(), project: f.projectOf(l.Cwd)}
	if f.opts.Project != "" && lc.project != f.opts.Project {
		return nil
	}
	if l.IsSidechain || (l.Type != "user" && l.Type != "assistant") {
		f.sum.Skipped++
		return nil
	}
	if f.skip {
		return nil
	}
	if !f.promptChecked && l.Type == "user" {
		if text, ok := promptText(l.Message.Content); ok {
			f.promptChecked = true
			f.skip = strings.HasPrefix(text, consolidate.PromptHeader)
			if f.skip {
				return nil
			}
		}
	}
	if !f.started[l.SessionID] {
		f.started[l.SessionID] = true
		if err := f.emit(ctx, newEvent(lc, model.SessionStart, 0)); err != nil {
			return err
		}
	}
	if l.Type == "assistant" {
		f.assistant(lc)
		return nil
	}
	return f.user(ctx, lc)
}

func (f *fileImport) projectOf(cwd string) string {
	if p, ok := f.projects[cwd]; ok {
		return p
	}
	p := capture.ResolveProject(cwd)
	f.projects[cwd] = p
	return p
}

func (f *fileImport) emit(ctx context.Context, e model.Event) error {
	if err := f.sink(ctx, e); err != nil {
		return err
	}
	f.sum.Events++
	return nil
}

// assistant remembers each tool call so its result can be normalized later.
func (f *fileImport) assistant(lc lineCtx) {
	blocks, _ := blocksOf(lc.l.Message.Content)
	for _, b := range blocks {
		if b.Type == "tool_use" {
			f.pending[b.ID] = toolUse{name: b.Name, input: b.Input}
		}
	}
}

func (f *fileImport) user(ctx context.Context, lc lineCtx) error {
	if text, ok := promptText(lc.l.Message.Content); ok {
		return f.prompt(ctx, lc, text)
	}
	blocks, _ := blocksOf(lc.l.Message.Content)
	for i, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		if err := f.toolResult(ctx, lc, i, b); err != nil {
			return err
		}
	}
	return nil
}

// prompt emits a turn_end before every prompt except the session's first.
func (f *fileImport) prompt(ctx context.Context, lc lineCtx, text string) error {
	s := lc.l.SessionID
	if f.prompted[s] {
		if err := f.emit(ctx, newEvent(lc, model.TurnEnd, 0)); err != nil {
			return err
		}
	}
	f.prompted[s] = true
	e := newEvent(lc, model.Prompt, 0)
	e.PromptText = text
	return f.emit(ctx, e)
}

func (f *fileImport) toolResult(ctx context.Context, lc lineCtx, index int, b block) error {
	call, ok := f.pending[b.ToolUseID]
	if !ok {
		return nil
	}
	delete(f.pending, b.ToolUseID)
	tool, payload := claudecode.NormalizeTool(call.name, call.input, b.IsError, contentText(b.Content))
	e := newEvent(lc, model.PostTool, index)
	e.Tool, e.Payload = tool, payload
	return f.emit(ctx, e)
}

// newEvent builds the event for a line. Its digest is fixed by the transcript
// identity, so a re-import hits the store's digest check.
func newEvent(lc lineCtx, kind model.EventKind, index int) model.Event {
	return model.Event{
		TS: lc.ts, Agent: agentName, Session: lc.l.SessionID, Project: lc.project, Kind: kind,
		Payload: map[string]any{}, Digest: digestOf(lc.l.SessionID, lc.l.UUID, kind, index),
	}
}

func digestOf(session, uuid string, kind model.EventKind, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("cc-import|%s|%s|%s|%d", session, uuid, kind, index)))
	return hex.EncodeToString(h[:])
}

// promptText returns a user message's text when it is a prompt: a string, or a
// list made only of text blocks.
func promptText(content json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s, s != ""
	}
	blocks, ok := blocksOf(content)
	if !ok || len(blocks) == 0 {
		return "", false
	}
	for _, b := range blocks {
		if b.Type != "text" {
			return "", false
		}
	}
	return joinText(blocks), true
}

// contentText is a tool result's text: a string, or the joined text blocks.
func contentText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	blocks, _ := blocksOf(content)
	return joinText(blocks)
}

func blocksOf(content json.RawMessage) ([]block, bool) {
	var blocks []block
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}

func joinText(blocks []block) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

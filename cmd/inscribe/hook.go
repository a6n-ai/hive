package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/agent"
	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/extract"
	"github.com/a6n-ai/inscribe/internal/feedback"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

const maxHookInput = 8 << 20

// hookCmd always exits 0 with no output on failure, so a broken Inscribe never
// breaks the agent. Errors go to inscribe.log instead.
func hookCmd(args []string, stdin io.Reader, stdout io.Writer) int {
	if os.Getenv("INSCRIBE_DISABLED") == "1" {
		return 0
	}
	home := config.Home()
	defer func() {
		if r := recover(); r != nil {
			logLine(home, "hook panic: %v", r)
		}
	}()
	if err := runHook(args, stdin, stdout); err != nil {
		logLine(home, "hook: %v", err)
	}
	return 0
}

func runHook(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	agentName := fs.String("agent", "", "agent adapter name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: hook --agent NAME EVENT")
	}
	event := fs.Arg(0)
	ad, err := agent.Get(*agentName)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxHookInput))
	if err != nil {
		return err
	}
	e, err := ad.Parse(event, raw)
	if err != nil {
		return err
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe", Project: e.Project})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	ctx := context.Background()
	h.Prepare(&e)
	if err := h.Capture(ctx, e); err != nil {
		return err
	}
	if e.Kind == model.SessionStart {
		// Before training and catch-up start, so they see sessions that ended without a SessionEnd.
		if err := closeStoppedSessions(ctx, h, e.Session, time.Now()); err != nil {
			logLine(config.Home(), "close sessions: %v", err)
		}
	}
	if err := followUps(event, e, h.Consolidation().Auto); err != nil {
		logLine(config.Home(), "follow-up: %v", err)
	}
	saved, err := h.SavedBy(e.Session, e.TS)
	if err != nil {
		logLine(config.Home(), "saved: %v", err)
	}
	q, ok := hookQuery(e, h.Scrub)
	if !ok {
		out, asked := askFeedback(ctx, h, ad, event, e, saved, nil)
		logDroppedInjections(h)
		return writeDebug(stdout, h, ad, event, e, out, nil, saved, asked)
	}
	lines, err := h.Recall(ctx, q)
	if err != nil {
		return err
	}
	if n := h.DroppedOutcomes(); n > 0 {
		logLine(config.Home(), "recall: dropped %d outcome rows (store locked)", n)
	}
	if e.Kind == model.SessionStart {
		rs, err := h.RecentSessions(e.Session, recentScan)
		if err != nil {
			logLine(config.Home(), "recent sessions: %v", err)
		}
		lines = append(lines, recentLines(rs, e.Project, time.Now())...)
		if h.StatsConfig().SessionLine {
			if s, err := h.SessionLine(e.Project); err != nil {
				logLine(config.Home(), "session line: %v", err)
			} else {
				lines = append(lines, recall.Line{Type: "inscribe", Text: s})
			}
		}
	}
	ids := make([]string, 0, len(lines))
	for _, l := range lines {
		if l.MemoryID != "" {
			ids = append(ids, l.MemoryID)
		}
	}
	logLine(config.Home(), "%s %s %s: injected %d line(s) %v", e.Agent, event, e.Project, len(lines), ids)
	if hook, ok := injectionHooks[e.Kind]; ok && len(lines) > 0 {
		in := store.InjectionInput{
			TS: e.TS, Session: e.Session, Agent: e.Agent, Project: e.Project,
			Hook: hook, LineCount: len(lines),
			EstTokens: recall.EstimateTokens(recall.Render(lines)), MemoryIDs: ids,
		}
		switch e.Kind {
		case model.Prompt:
			in.PromptDigest = e.Digest
		case model.PostTool:
			in.Tool = e.Tool
		}
		if err := h.RecordInjection(ctx, in); err != nil {
			logLine(config.Home(), "record injection: %v", err)
		}
	}
	out, asked := askFeedback(ctx, h, ad, event, e, saved, ad.Reply(event, lines))
	logDroppedInjections(h)
	return writeDebug(stdout, h, ad, event, e, out, lines, saved, asked)
}

func logDroppedInjections(h *inscribe.Inscribe) {
	if n := h.DroppedInjections(); n > 0 {
		logLine(config.Home(), "analytics: dropped %d injection rows (store locked)", n)
	}
}

var injectionHooks = map[model.EventKind]string{
	model.SessionStart: "SessionStart",
	model.Prompt:       "UserPromptSubmit",
	model.PostTool:     "PostToolUseFailure",
}

// askEvents are the Claude Code events whose reply can carry additionalContext.
// Stop cannot, so an ask made there would be recorded but never shown.
var askEvents = map[string]bool{"UserPromptSubmit": true, "PostToolUse": true, "PostToolUseFailure": true}

// askFeedback adds the feedback question to a Claude Code reply when a save makes
// one due. It returns the note that says so, for debug output. A failed ask is logged, never returned.
func askFeedback(ctx context.Context, h *inscribe.Inscribe, ad agent.Adapter, event string, e inscribe.Event, saved []inscribe.Memory, out []byte) ([]byte, askInfo) {
	if ad.Name() != "claude-code" || !askEvents[event] {
		return out, askInfo{}
	}
	askID, text, ids, _, err := h.MaybeAsk(ctx, e.Session, len(saved) > 0, time.Now())
	if err != nil {
		logLine(config.Home(), "feedback ask: %v", err)
		return out, askInfo{}
	}
	if askID == "" {
		return out, askInfo{}
	}
	in := store.InjectionInput{
		TS: e.TS, Session: e.Session, Agent: e.Agent, Project: e.Project,
		Hook: "feedback_ask", LineCount: len(ids),
		EstTokens: recall.EstimateTokens(text), MemoryIDs: ids,
	}
	if err := h.RecordInjection(ctx, in); err != nil {
		logLine(config.Home(), "record injection: %v", err)
	}
	return agent.AddContext(out, event, text), askInfo{ID: askID, Text: text, MemoryIDs: ids}
}

// askInfo describes a feedback ask added to a reply. The zero value means none.
type askInfo struct {
	ID        string
	Text      string
	MemoryIDs []string
}

// writeDebug writes the adapter's reply. In debug mode it first logs what the
// event loaded and saved, and adds that to the status line agents show the user.
func writeDebug(stdout io.Writer, h *inscribe.Inscribe, ad agent.Adapter, event string, e inscribe.Event, out []byte, lines []recall.Line, saved []inscribe.Memory, asked askInfo) error {
	if h.Debug() {
		if note := debugNote(lines, saved, e.TS); note != "" {
			if asked.ID != "" {
				note += fmt.Sprintf("\n  feedback asked %s (%d memories)", asked.ID, len(asked.MemoryIDs))
			}
			logLine(config.Home(), "debug %s %s %s: %s", e.Agent, event, e.Project, note)
			if ad.Capabilities().ShowsStatus {
				out = agent.AddStatus(out, note)
			}
		}
	}
	if out == nil {
		return nil
	}
	_, err := stdout.Write(out)
	return err
}

const (
	debugExamples = 3  // memories listed per kind
	debugRunes    = 90 // each listed memory is cut to this many runes
)

// debugNote summarizes what one event loaded and saved, with a few examples.
// A memory first seen at ts was created by this event; others were updated.
func debugNote(lines []recall.Line, saved []inscribe.Memory, ts int64) string {
	var loaded []string
	for _, l := range lines {
		if l.Type != "recent" {
			loaded = append(loaded, fmt.Sprintf("[%s] %s", l.Type, l.Text))
		}
	}
	created, out := 0, []string{}
	for _, m := range saved {
		verb := "updated"
		if m.FirstSeen == ts {
			verb, created = "new", created+1
		}
		out = append(out, fmt.Sprintf("%s [%s] %s", verb, m.Type, m.Text))
	}
	if len(loaded) == 0 && len(out) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "inscribe debug: loaded %d, saved %d (%d new)", len(loaded), len(saved), created)
	for _, s := range loaded[:min(len(loaded), debugExamples)] {
		b.WriteString("\n  loaded " + feedback.CutRunes(s, debugRunes))
	}
	for _, s := range out[:min(len(out), debugExamples)] {
		b.WriteString("\n  saved " + feedback.CutRunes(s, debugRunes))
	}
	return b.String()
}

// stallAfter is how long a session may stay silent before it counts as ended,
// even while its process runs: a tab left open or a hung process.
const stallAfter = time.Hour

// closeStoppedSessions ends sessions whose agent stopped without a SessionEnd
// event: silent for stallAfter, or with no live process when the agent can
// tell. A later event in a session reopens it.
func closeStoppedSessions(ctx context.Context, h *inscribe.Inscribe, current string, now time.Time) error {
	open, err := h.OpenSessions()
	if err != nil {
		return err
	}
	live := map[string]map[string]bool{}
	var ids []string
	for _, s := range open {
		if s.ID == current {
			continue
		}
		if now.Sub(time.UnixMilli(s.LastEvent)) >= stallAfter {
			ids = append(ids, s.ID)
			continue
		}
		known, seen := live[s.Agent]
		if !seen {
			known = liveSessionsOf(s.Agent)
			live[s.Agent] = known
		}
		if known != nil && !known[s.ID] {
			ids = append(ids, s.ID)
		}
	}
	return h.CloseSessions(ctx, ids)
}

// liveSessionsOf returns the agent's sessions with a running process, or nil
// when the agent cannot tell.
func liveSessionsOf(name string) map[string]bool {
	ad, err := agent.Get(name)
	if err != nil {
		return nil
	}
	ls, ok := ad.(agent.LiveSessioner)
	if !ok {
		return nil
	}
	if m, ok := ls.LiveSessions(); ok {
		return m
	}
	return nil
}

// followUps starts the background work an event implies. The hook never waits for it.
// Consolidation needs the agent to run headless, so it is skipped for agents
// that cannot.
func followUps(event string, e inscribe.Event, auto bool) error {
	if ad, err := agent.Get(e.Agent); err == nil && len(ad.Headless("").Command) == 0 {
		auto = false
	}
	switch event {
	case "SessionStart":
		if err := spawnDetached("retention", "auto"); err != nil {
			return err
		}
		// The previous session is idle. Update the score model before this one recalls.
		if err := spawnDetached("score", "train"); err != nil {
			return err
		}
		if auto {
			return spawnDetached("consolidate", "--catch-up", "--agent", e.Agent)
		}
	case "SessionEnd":
		// The agent stopped. Label what it just did. This pass does not call a model.
		if err := spawnDetached("score", "train"); err != nil {
			return err
		}
		if auto {
			return spawnDetached("consolidate", "--session", e.Session, "--agent", e.Agent)
		}
	}
	return nil
}

// hookQuery picks what to recall for an event by its kind, so every agent's
// event names reach the same recall. ok is false when the event should inject nothing.
func hookQuery(e inscribe.Event, scrub func(string) string) (inscribe.Query, bool) {
	switch e.Kind {
	case model.SessionStart:
		return inscribe.Query{Project: e.Project, Mode: recall.SessionStart}, true
	case model.Prompt:
		return inscribe.Query{Project: e.Project, Text: e.PromptText, Agent: e.Agent, Session: e.Session, PromptDigest: e.Digest, Mode: recall.Prompt}, true
	case model.PostTool:
		errText, _ := e.Payload["error"].(string)
		if errText == "" {
			return inscribe.Query{}, false
		}
		return inscribe.Query{Project: e.Project, ErrorSignature: extract.Signature(scrub(errText)), Mode: recall.PostToolError}, true
	}
	return inscribe.Query{}, false
}

const (
	recentScan       = 30  // sessions read to fill both lists below
	recentPerList    = 3   // sessions shown for this project, and for other projects
	recentPromptRune = 100 // a prompt is cut to this many runes
)

// recentLines lists what the user last asked in recent sessions, this
// project's first, so a new session knows where work left off.
func recentLines(rs []inscribe.RecentSession, project string, now time.Time) []recall.Line {
	var here, other []recall.Line
	for _, r := range rs {
		where := "this project"
		if r.Project != project {
			where = filepath.Base(r.Project)
		}
		p := []rune(strings.Join(strings.Fields(r.LastPrompt), " "))
		if len(p) > recentPromptRune {
			p = append(p[:recentPromptRune], '…')
		}
		l := recall.Line{Type: "recent", Text: fmt.Sprintf("%s, %s ago: %s", where, ago(now.Sub(time.UnixMilli(r.LastEvent))), string(p))}
		if r.Project == project && len(here) < recentPerList {
			here = append(here, l)
		} else if r.Project != project && len(other) < recentPerList {
			other = append(other, l)
		}
	}
	return append(here, other...)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 1))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// logLine appends one timestamped line to inscribe.log in home. Errors writing
// the log are dropped: there is nowhere left to report them.
func logLine(home, format string, args ...any) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "inscribe.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the log path is under the user's INSCRIBE_HOME
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	msg := strings.Join(strings.Fields(fmt.Sprintf(format, args...)), " ")
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), msg)
}

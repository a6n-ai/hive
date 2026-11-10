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

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/extract"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
)

const maxHookInput = 8 << 20

// hookCmd always exits 0 with no output on failure, so a broken Hive never
// breaks the agent. Errors go to hive.log instead.
func hookCmd(args []string, stdin io.Reader, stdout io.Writer) int {
	if os.Getenv("HIVE_DISABLED") == "1" {
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
	h, err := hive.Open(hive.Options{Caller: "hive", Project: e.Project})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	ctx := context.Background()
	if err := h.Capture(ctx, e); err != nil {
		return err
	}
	if err := followUps(event, e, h.Consolidation().Auto); err != nil {
		logLine(config.Home(), "follow-up: %v", err)
	}
	q, ok := hookQuery(e, h.Scrub)
	if !ok {
		return nil
	}
	lines, err := h.Recall(ctx, q)
	if err != nil {
		return err
	}
	if out := ad.Reply(event, lines); out != nil {
		_, err = stdout.Write(out)
	}
	return err
}

// followUps starts the background work an event implies. The hook never waits for it.
func followUps(event string, e hive.Event, auto bool) error {
	switch event {
	case "SessionStart":
		if err := spawnDetached("retention", "auto"); err != nil {
			return err
		}
		if auto {
			return spawnDetached("consolidate", "--catch-up", "--agent", e.Agent)
		}
	case "SessionEnd":
		if auto {
			return spawnDetached("consolidate", "--session", e.Session, "--agent", e.Agent)
		}
	}
	return nil
}

// hookQuery picks what to recall for an event by its kind, so every agent's
// event names reach the same recall. ok is false when the event should inject nothing.
func hookQuery(e hive.Event, scrub func(string) string) (hive.Query, bool) {
	switch e.Kind {
	case model.SessionStart:
		return hive.Query{Project: e.Project, Mode: recall.SessionStart}, true
	case model.Prompt:
		return hive.Query{Project: e.Project, Text: e.PromptText, Mode: recall.Prompt}, true
	case model.PostTool:
		errText, _ := e.Payload["error"].(string)
		if errText == "" {
			return hive.Query{}, false
		}
		return hive.Query{Project: e.Project, ErrorSignature: extract.Signature(scrub(errText)), Mode: recall.PostToolError}, true
	}
	return hive.Query{}, false
}

// logLine appends one timestamped line to hive.log in home. Errors writing
// the log are dropped: there is nowhere left to report them.
func logLine(home, format string, args ...any) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "hive.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the log path is under the user's HIVE_HOME
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	msg := strings.Join(strings.Fields(fmt.Sprintf(format, args...)), " ")
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), msg)
}

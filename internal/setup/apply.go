package setup

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/agent"
	"github.com/a6n-ai/inscribe/internal/importer"
)

// Runner runs an external command and returns its output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

type Failure struct {
	Action Action
	Err    error
}

type Report struct {
	Done   []Action
	Failed []Failure
}

// Apply runs each action in order. A failed action is recorded and the rest still run.
func Apply(ctx context.Context, env Env, actions []Action, run Runner) Report {
	var rep Report
	for _, a := range actions {
		if err := applyOne(ctx, env, a, run); err != nil {
			rep.Failed = append(rep.Failed, Failure{Action: a, Err: err})
			continue
		}
		rep.Done = append(rep.Done, a)
	}
	return rep
}

func applyOne(ctx context.Context, env Env, a Action, run Runner) error {
	switch a.Kind {
	case "install-hooks":
		ad, err := agent.Get(a.Agent)
		if err != nil {
			return err
		}
		return ad.Install(a.Files[0], env.InscribeBin)
	case "disable-plugin":
		_, err := run(ctx, a.Command[0], a.Command[1:]...)
		return err
	case "import":
		return importHistory(ctx, a.Since, time.Now())
	case "write-config":
		return SetConfigKeys(a.Files[0], a.Keys)
	case "mcp-add":
		return addMCP(ctx, a.Command, run)
	}
	return fmt.Errorf("unknown action %q", a.Kind)
}

// addMCP adds the Inscribe MCP server at user scope unless `claude mcp get inscribe` finds it.
func addMCP(ctx context.Context, add []string, run Runner) error {
	if _, err := run(ctx, "claude", "mcp", "get", "inscribe"); err == nil {
		return nil
	}
	_, err := run(ctx, add[0], add[1:]...)
	return err
}

func importHistory(ctx context.Context, since string, now time.Time) error {
	from, err := sinceDate(since, now)
	if err != nil {
		return err
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	_, err = h.ImportClaudeCode(ctx, importer.Options{Since: from})
	return err
}

// sinceDate maps an import choice ("30d", "90d", "all") to the earliest date imported.
func sinceDate(since string, now time.Time) (time.Time, error) {
	if since == "all" {
		return time.Time{}, nil
	}
	days, err := strconv.Atoi(strings.TrimSuffix(since, "d"))
	if err != nil {
		return time.Time{}, fmt.Errorf("import since %q: want 30d, 90d, or all", since)
	}
	return now.AddDate(0, 0, -days), nil
}

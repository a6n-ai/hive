package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
)

const syncHeading = "## Hive memory (generated; edits here are replaced)"

const syncReview = "This file may be committed to your repository; review the block before committing."

// syncCmd writes the project's session-start recall into the agent's
// instructions file, for agents that have no hooks to inject it.
func syncCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	agentName := fs.String("agent", "", "agent whose instructions file to write")
	project := fs.String("project", "", "project directory (default: the current project)")
	includeGlobal := fs.Bool("include-global", false, "also write global memories, which may be private to you")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ad, err := agent.Get(*agentName)
	if err != nil {
		return err
	}
	proj := *project
	if proj == "" {
		proj = defaultProject()
	}
	h, err := hive.Open(hive.Options{Caller: "hive", Project: proj})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	ctx := context.Background()
	ms, err := h.List(ctx, hive.ListFilter{Statuses: []model.Status{model.Active}})
	if err != nil {
		return err
	}
	lines, err := h.Recall(ctx, hive.Query{Project: proj, Mode: recall.SessionStart, Budget: 400})
	if err != nil {
		return err
	}
	allowed := syncableIDs(ms, proj, *includeGlobal)
	var kept []hive.Line
	for _, l := range lines {
		if allowed[l.MemoryID] {
			l.Text = h.Scrub(l.Text)
			kept = append(kept, l)
		}
	}
	path := filepath.Join(proj, ad.InstructionsFile())
	if err := agent.WriteBlock(path, syncHeading+"\n\n"+recall.Render(kept)); err != nil {
		return err
	}
	note := syncReview
	if *includeGlobal {
		note = "--include-global is on: global memories are in the block. " + syncReview
	}
	_, err = fmt.Fprintf(stdout, "synced %s\n%s\n", path, note)
	return err
}

// syncableIDs are the memories a committable file may show: this project's
// project- and file-scoped memories, plus global ones when asked for.
func syncableIDs(ms []hive.Memory, proj string, includeGlobal bool) map[string]bool {
	ok := map[string]bool{}
	for _, m := range ms {
		switch m.ScopeKind {
		case model.ProjectScope:
			ok[m.ID] = m.Scope == proj
		case model.File:
			ok[m.ID] = strings.HasPrefix(m.Scope, proj+string(filepath.Separator))
		case model.Global:
			ok[m.ID] = includeGlobal
		}
	}
	return ok
}

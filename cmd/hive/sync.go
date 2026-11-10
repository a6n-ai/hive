package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/recall"
)

const syncHeading = "## Hive memory (generated; edits here are replaced)"

// syncCmd writes the project's session-start recall into the agent's
// instructions file, for agents that have no hooks to inject it.
func syncCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	agentName := fs.String("agent", "", "agent whose instructions file to write")
	project := fs.String("project", "", "project directory (default: the current project)")
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
	lines, err := h.Recall(context.Background(), hive.Query{Project: proj, Mode: recall.SessionStart, Budget: 400})
	if err != nil {
		return err
	}
	path := filepath.Join(proj, ad.InstructionsFile())
	if err := agent.WriteBlock(path, syncHeading+"\n\n"+recall.Render(lines)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "synced %s\nThis file may be committed to your repository; review the block before committing.\n", path)
	return err
}

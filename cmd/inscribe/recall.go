package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/recall"
)

func recallCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	project := fs.String("project", defaultProject(), "project path")
	query := fs.String("query", "", "prompt text to recall for; empty recalls for session start")
	budget := fs.Int("budget", 0, "token budget (0 uses the configured default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe", Project: *project})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	mode := recall.SessionStart
	if *query != "" {
		mode = recall.Prompt
	}
	lines, err := h.Recall(context.Background(), inscribe.Query{Project: *project, Text: *query, Budget: *budget, Mode: mode})
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Fprintf(stdout, "%s [%s] %s\n", l.MemoryID, l.Type, l.Text)
	}
	return nil
}

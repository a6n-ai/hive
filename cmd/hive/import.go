package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/internal/importer"
)

func importCmd(args []string, stdout io.Writer) error {
	const usageText = "usage: hive import claude-code [--since YYYY-MM-DD] [--project P] [--root DIR]"
	if len(args) == 0 || args[0] != "claude-code" {
		return errors.New(usageText)
	}
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	since := fs.String("since", "", "import only lines on or after this date (YYYY-MM-DD)")
	project := fs.String("project", "", "import only this project path")
	root := fs.String("root", "", "transcripts directory (default ~/.claude/projects)")
	if _, err := parseInterspersed(fs, args[1:]); err != nil {
		return err
	}
	o := importer.Options{Root: *root, Project: *project}
	if *since != "" {
		t, err := time.Parse(time.DateOnly, *since)
		if err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		o.Since = t
	}
	h, err := hive.Open(hive.Options{Caller: "hive"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	rep, err := h.ImportClaudeCode(context.Background(), o)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "files=%d lines=%d skipped=%d events=%d new_events=%d new_memories=%d secrets_redacted=%d\n",
		rep.Files, rep.Lines, rep.Skipped, rep.Events, rep.NewEvents, rep.NewMemories, rep.Redactions)
	return err
}

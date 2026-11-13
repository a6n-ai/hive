package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/importer"
)

func importCmd(args []string, stdout io.Writer) error {
	const usageText = "usage: inscribe import claude-code [--since YYYY-MM-DD] [--project P] [--root DIR] | claude-mem [--db PATH] [--types decision,bugfix] [--project PATH] [--since YYYY-MM-DD] [--dry-run]"
	if len(args) > 0 && args[0] == "claude-mem" {
		return importClaudeMemCmd(args[1:], stdout)
	}
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
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
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

func importClaudeMemCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("import claude-mem", flag.ContinueOnError)
	db := fs.String("db", "", "claude-mem database (default ~/.claude-mem/claude-mem.db)")
	types := fs.String("types", "", "claude-mem types to import, comma-separated (default decision,bugfix)")
	project := fs.String("project", "", "import only observations from this session cwd")
	since := fs.String("since", "", "import only observations on or after this date (YYYY-MM-DD)")
	dryRun := fs.Bool("dry-run", false, "count what would be imported without writing")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	o := importer.ClaudeMemOptions{DB: *db, Project: *project, DryRun: *dryRun}
	if *types != "" {
		o.Types = strings.Split(*types, ",")
	}
	if *since != "" {
		t, err := time.Parse(time.DateOnly, *since)
		if err != nil {
			return fmt.Errorf("--since: %w", err)
		}
		o.Since = t
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	rep, err := h.ImportClaudeMem(context.Background(), o)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "read=%d imported=%d skipped=%d\n", rep.Read, rep.Imported, rep.Skipped); err != nil {
		return err
	}
	for _, r := range slices.Sorted(maps.Keys(rep.SkippedByReason)) {
		if _, err := fmt.Fprintf(stdout, "skipped.%s=%d\n", r, rep.SkippedByReason[r]); err != nil {
			return err
		}
	}
	return nil
}

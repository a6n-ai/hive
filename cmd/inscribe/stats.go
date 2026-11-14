package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/config"
)

func statsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "ingest" {
		return statsReport(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("stats ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	catchUp := fs.Bool("catch-up", false, "ingest usage of finished sessions")
	limit := fs.Int("limit", 20, "most sessions to ingest")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if !*catchUp {
		fmt.Fprintln(stderr, "usage: inscribe stats ingest --catch-up [--limit N]")
		return 2
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		fmt.Fprintln(stderr, "inscribe:", err)
		return 1
	}
	defer func() { _ = h.Close() }()
	n, err := h.IngestUsage(context.Background(), *limit)
	if err != nil {
		logLine(config.Home(), "stats ingest: %v", err)
	}
	fmt.Fprintf(stdout, "ingested %d session(s)\n", n)
	return 0
}

func statsReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	days := fs.Int("days", 7, "window in days")
	all := fs.Bool("all", false, "all projects")
	explain := fs.Bool("explain", false, "print the estimate model and its biases")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *days <= 0 {
		fmt.Fprintln(stderr, "usage: inscribe stats [--days N] [--all] [--explain] [--json]")
		return 2
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		fmt.Fprintln(stderr, "inscribe:", err)
		return 1
	}
	defer func() { _ = h.Close() }()
	out, err := h.Stats(context.Background(), inscribe.StatsOptions{Days: *days, Project: defaultProject(), All: *all, Explain: *explain, JSON: *asJSON})
	if err != nil {
		fmt.Fprintln(stderr, "inscribe:", err)
		return 1
	}
	fmt.Fprint(stdout, out)
	return 0
}

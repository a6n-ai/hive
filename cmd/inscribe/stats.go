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
		fmt.Fprintln(stderr, "stats: report not built yet")
		return 2
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

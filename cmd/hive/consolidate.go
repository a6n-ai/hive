package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/a6n-ai/hive"
	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/consolidate"
)

const consolidateUsage = "usage: hive consolidate (--session S | --catch-up) [--agent A] [--dry-run]"

func consolidateCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("consolidate", flag.ContinueOnError)
	session := fs.String("session", "", "consolidate this session's new events")
	catchUp := fs.Bool("catch-up", false, "consolidate every idle session with enough new events")
	agentName := fs.String("agent", "claude-code", "agent whose headless command runs the review")
	dry := fs.Bool("dry-run", false, "print the proposed ops without applying them")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if (*session != "") == *catchUp {
		return errors.New(consolidateUsage)
	}
	ad, err := agent.Get(*agentName)
	if err != nil {
		return err
	}
	h, err := hive.Open(hive.Options{Caller: "hive"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	runner := consolidate.Headless{Spec: headlessSpec(ad, h.Consolidation().Model[*agentName])}
	sessions := []string{*session}
	if *catchUp {
		if sessions, err = h.DueConsolidations(*agentName, time.Now()); err != nil {
			return err
		}
	}
	for _, s := range sessions {
		rep, err := h.Consolidate(context.Background(), s, runner, *dry)
		if err != nil {
			return err
		}
		if err := writeRun(stdout, s, rep, *dry); err != nil {
			return err
		}
	}
	return nil
}

// headlessSpec is the adapter's headless command. HIVE_TEST_HEADLESS, when set,
// replaces the executable: a test-only override so tests never call a real model.
func headlessSpec(ad agent.Adapter, model string) agent.HeadlessSpec {
	spec := ad.Headless(model)
	if p := os.Getenv("HIVE_TEST_HEADLESS"); p != "" {
		spec.Command = []string{p}
	}
	return spec
}

func writeRun(w io.Writer, session string, rep consolidate.RunReport, dry bool) error {
	applied, would := len(rep.Valid), 0
	if dry {
		applied, would = 0, len(rep.Valid)
	}
	_, err := fmt.Fprintf(w, "session=%s run=%s status=%s applied=%d rejected=%d would_apply=%d\n",
		session, rep.RunID, rep.Status, applied, len(rep.Rejected), would)
	return err
}

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/retention"
)

const retentionUsage = "usage: inscribe retention apply [--yes] | inscribe retention auto"

func retentionCmd(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(retentionUsage)
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	ctx := context.Background()
	switch args[0] {
	case "auto":
		_, err := h.AutoRetention(ctx)
		return err
	case "apply":
		return retentionApply(ctx, h, args[1:], stdin, stdout)
	}
	return errors.New(retentionUsage)
}

// retentionApply shows a dry-run count, then applies it after a y answer (or at once with --yes).
func retentionApply(ctx context.Context, h *inscribe.Inscribe, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("retention apply", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "apply without asking")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	dry, err := h.Retention(ctx, true)
	if err != nil {
		return err
	}
	if err := writeReport(stdout, "dry run", dry); err != nil {
		return err
	}
	if !*yes {
		if _, err := fmt.Fprint(stdout, "Apply these changes? [y/N] "); err != nil {
			return err
		}
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			_, err := fmt.Fprintln(stdout, "not applied")
			return err
		}
	}
	rep, err := h.Retention(ctx, false)
	if err != nil {
		return err
	}
	return writeReport(stdout, "applied", rep)
}

func writeReport(w io.Writer, label string, r retention.Report) error {
	_, err := fmt.Fprintf(w, "%s: prompt_texts_cleared=%d events_deleted=%d replay_added=%d replay_dropped=%d kept_as_evidence=%d injections_deleted=%d usage_turns_deleted=%d\n",
		label, r.PromptTextsCleared, r.EventsDeleted, r.ReplayAdded, r.ReplayDropped, r.KeptAsEvidence, r.InjectionsDeleted, r.UsageTurnsDeleted)
	return err
}

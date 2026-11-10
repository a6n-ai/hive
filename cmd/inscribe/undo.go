package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/a6n-ai/inscribe"
)

func undoCmd(args []string, stdout io.Writer) error {
	pos, err := parseInterspersed(flag.NewFlagSet("undo", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: inscribe undo RUN_ID")
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	rep, err := h.Undo(context.Background(), pos[0])
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "restored=%d skipped=%d\n", rep.Restored, len(rep.Skipped)); err != nil {
		return err
	}
	for _, id := range rep.Skipped {
		if _, err := fmt.Fprintf(stdout, "skipped %s: changed since the run\n", id); err != nil {
			return err
		}
	}
	return nil
}

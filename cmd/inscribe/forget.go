package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/a6n-ai/inscribe"
)

func forgetCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "delete the memory and its evidence instead of marking it forgotten")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: inscribe forget ID [--purge]")
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	if err := h.Forget(context.Background(), pos[0], *purge); err != nil {
		return err
	}
	verb := "forgotten"
	if *purge {
		verb = "purged"
	}
	_, err = fmt.Fprintf(stdout, "%s %s\n", verb, pos[0])
	return err
}

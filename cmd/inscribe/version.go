package main

import (
	"fmt"
	"io"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionCmd(stdout io.Writer) error {
	_, err := fmt.Fprintf(stdout, "inscribe %s (%s, %s)\n", version, commit, date)
	return err
}

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnDetached re-runs this binary with args in a new session and returns
// without waiting for it. Its output goes to the null device.
var spawnDetached = func(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = devnull.Close() }()
	cmd := exec.Command(exe, args...) //nolint:gosec // G204: re-runs this binary with fixed subcommands
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

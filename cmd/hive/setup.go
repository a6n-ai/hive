package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/a6n-ai/hive/internal/setup"
)

// setupCmd detects the machine, collects choices, and applies them. It returns
// the exit code: 2 when there is no terminal and no --yes, 1 when a step failed.
func setupCmd(args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "apply the defaults without prompts")
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	env, err := setupEnv()
	if err != nil {
		return 1, err
	}
	d, err := setup.Detect(env)
	if err != nil {
		return 1, err
	}
	c := setup.Defaults(d)
	switch {
	case !*yes && isTerminal(stdin):
		ok, err := runSetupForm(stdout, d, &c)
		if err != nil {
			return 1, err
		}
		if !ok {
			_, err := fmt.Fprintln(stdout, "setup cancelled; nothing changed")
			return 0, err
		}
	case !*yes:
		printPlan(stdout, d, c)
		_, err := fmt.Fprintln(stdout, "run 'hive setup --yes' to apply these defaults")
		return 2, err
	default:
		printPlan(stdout, d, c)
	}
	return applySetup(stdout, env, d, c)
}

func setupEnv() (setup.Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return setup.Env{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		return setup.Env{}, err
	}
	return setup.Env{Home: home, HiveBin: bin, LookPath: exec.LookPath, ReadFile: os.ReadFile}, nil
}

// isTerminal reports whether r is a character device, such as a terminal.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func printPlan(w io.Writer, d setup.Detected, c setup.Choices) {
	for _, a := range setup.Plan(d, c) {
		fmt.Fprintf(w, "- %s\n", a.Desc)
		for _, f := range a.Files {
			fmt.Fprintf(w, "    file: %s\n", f)
		}
		if len(a.Command) > 0 {
			fmt.Fprintf(w, "    command: %s\n", strings.Join(a.Command, " "))
		}
	}
	for _, msg := range setup.Warnings(d, c) {
		fmt.Fprintf(w, "warning: %s\n", msg)
	}
}

func applySetup(stdout io.Writer, env setup.Env, d setup.Detected, c setup.Choices) (int, error) {
	rep := setup.Apply(context.Background(), env, setup.Plan(d, c), execRunner)
	for _, a := range rep.Done {
		fmt.Fprintln(stdout, "done:", a.Desc)
	}
	for _, f := range rep.Failed {
		fmt.Fprintf(stdout, "failed: %s: %v\n", f.Action.Desc, f.Err)
	}
	if err := doctorCmd(stdout); err != nil {
		return 1, err
	}
	if len(rep.Failed) > 0 {
		return 1, nil
	}
	return 0, nil
}

// execRunner runs a command the user's own tools provide, such as claude.
func execRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: the commands are fixed by setup.Plan
	return string(out), err
}

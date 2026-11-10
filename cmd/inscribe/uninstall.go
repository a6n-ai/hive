package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/a6n-ai/inscribe/agent"
	"github.com/a6n-ai/inscribe/internal/config"
)

// uninstallCmd removes one agent's hooks with --agent. Without it, it removes
// the hooks of every agent and the Claude Code MCP server; --purge also deletes
// the data directory. Each step runs even when an earlier one fails.
func uninstallCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	agentName := fs.String("agent", "", "remove only this agent's hooks (claude-code, codex, gemini)")
	settings := fs.String("settings", "", "settings file for --agent (default is the agent's own)")
	purge := fs.Bool("purge", false, "also delete all Inscribe data: memories, events, config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *agentName != "" {
		if *purge {
			return errors.New("--purge removes everything; use it without --agent")
		}
		ad, err := agent.Get(*agentName)
		if err != nil {
			return err
		}
		path := *settings
		if path == "" {
			path = ad.SettingsPath()
		}
		return removeHooks(ad, path, stdout)
	}

	var errs []error
	for _, ad := range agent.All() {
		path := ad.SettingsPath()
		if _, err := os.Stat(path); err != nil {
			continue
		}
		errs = append(errs, removeHooks(ad, path, stdout))
	}
	errs = append(errs, removeMCP(stdout))
	if *purge {
		errs = append(errs, purgeData(stdout))
	} else {
		errs = append(errs, say(stdout, "kept data in %s (use --purge to delete it)", config.Home()))
	}
	return errors.Join(errs...)
}

func removeHooks(ad agent.Adapter, path string, stdout io.Writer) error {
	if err := ad.Uninstall(path); err != nil {
		return fmt.Errorf("%s hooks: %w", ad.Name(), err)
	}
	return say(stdout, "removed %s hooks from %s", ad.Name(), path)
}

// removeMCP unregisters the user-scope MCP server that setup adds to Claude Code.
func removeMCP(stdout io.Writer) error {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil
	}
	ctx := context.Background()
	if _, err := execRunner(ctx, "claude", "mcp", "get", "inscribe"); err != nil {
		return nil
	}
	if out, err := execRunner(ctx, "claude", "mcp", "remove", "--scope", "user", "inscribe"); err != nil {
		return fmt.Errorf("claude mcp remove: %w: %s", err, out)
	}
	return say(stdout, "removed the inscribe MCP server from Claude Code")
}

// purgeData deletes the data directory, refusing paths whose removal would take
// unrelated files with it.
func purgeData(stdout io.Writer) error {
	dir, err := filepath.Abs(config.Home())
	if err != nil {
		return err
	}
	if userHome, err := os.UserHomeDir(); dir == "/" || (err == nil && dir == filepath.Clean(userHome)) {
		return fmt.Errorf("refusing to delete %s: INSCRIBE_HOME points at a directory that is not Inscribe's own", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return say(stdout, "deleted %s", dir)
}

func say(w io.Writer, format string, a ...any) error {
	_, err := fmt.Fprintf(w, format+"\n", a...)
	return err
}

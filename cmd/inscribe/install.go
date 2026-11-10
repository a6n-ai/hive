package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/a6n-ai/inscribe/agent"
)

// settingsCmd parses the --agent and --settings flags shared by install and
// uninstall. The default settings file is the adapter's.
func settingsCmd(name string, args []string) (agent.Adapter, string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	agentName := fs.String("agent", "", "agent to hook (claude-code, codex, gemini)")
	settings := fs.String("settings", "", "settings file (default is the agent's own)")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	ad, err := agent.Get(*agentName)
	if err != nil {
		return nil, "", err
	}
	if *settings == "" {
		*settings = ad.SettingsPath()
	}
	return ad, *settings, nil
}

func installCmd(args []string, stdout io.Writer) error {
	ad, settings, err := settingsCmd("install", args)
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if err := ad.Install(settings, bin); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "installed %s hooks in %s\n", ad.Name(), settings); err != nil {
		return err
	}
	if n, ok := ad.(agent.InstallNoter); ok {
		_, err = fmt.Fprintln(stdout, n.InstallNote())
		return err
	}
	return nil
}

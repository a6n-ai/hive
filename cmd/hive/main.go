// Command hive is the Hive CLI: agent hooks and memory management.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/a6n-ai/hive/internal/capture"

	_ "github.com/a6n-ai/hive/agent/claudecode"
	_ "github.com/a6n-ai/hive/agent/codex"
	_ "github.com/a6n-ai/hive/agent/gemini"
)

const usage = `usage: hive <command> [args]
commands: hook, install, uninstall, recall, remember, forget, why, list, doctor, import, retention, consolidate, undo, sync`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "hook":
		return hookCmd(args[1:], stdin, stdout)
	case "install":
		err = installCmd(args[1:], stdout)
	case "uninstall":
		err = uninstallCmd(args[1:], stdout)
	case "recall":
		err = recallCmd(args[1:], stdout)
	case "remember":
		err = rememberCmd(args[1:], stdout)
	case "forget":
		err = forgetCmd(args[1:], stdout)
	case "why":
		err = whyCmd(args[1:], stdout)
	case "list":
		err = listCmd(args[1:], stdout)
	case "import":
		err = importCmd(args[1:], stdout)
	case "retention":
		err = retentionCmd(args[1:], stdin, stdout)
	case "consolidate":
		err = consolidateCmd(args[1:], stdout)
	case "undo":
		err = undoCmd(args[1:], stdout)
	case "sync":
		err = syncCmd(args[1:], stdout)
	case "doctor":
		err = doctorCmd(stdout)
	default:
		err = fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	if err != nil {
		fmt.Fprintln(stderr, "hive:", err)
		return 1
	}
	return 0
}

func defaultProject() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return capture.ResolveProject(cwd)
}

// parseInterspersed parses flags that may come before or after positional
// arguments, which the standard flag package does not do on its own.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

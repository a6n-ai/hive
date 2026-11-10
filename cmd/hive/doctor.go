package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/agent/hookjson"
	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/scrub"
	"github.com/a6n-ai/hive/internal/store"
)

// doctorAgents are the agents doctor reports on, in print order.
var doctorAgents = []string{"claude-code", "codex", "gemini"}

func doctorCmd(stdout io.Writer) error {
	home := config.Home()
	for _, name := range doctorAgents {
		if err := doctorAgent(stdout, name); err != nil {
			return err
		}
	}

	dbPath := filepath.Join(home, "hive.db")
	if st, err := store.Open(dbPath, true); err != nil {
		fmt.Fprintf(stdout, "database: %s: %v\n", dbPath, err)
	} else {
		v, verr := st.SchemaVersion()
		_ = st.Close()
		if verr != nil {
			return verr
		}
		fmt.Fprintf(stdout, "database: %s (schema version %d)\n", dbPath, v)
	}

	fmt.Fprintf(stdout, "scrubber: %s\n", scrubberSelfTest())
	entries, _ := os.ReadDir(filepath.Join(home, "spool"))
	fmt.Fprintf(stdout, "spool: %d file(s) pending\n", len(entries))
	return nil
}

// doctorAgent reports one agent's settings file: its Hive hooks, the warnings
// that apply to an agent with hooks installed, and any claude-mem hooks.
func doctorAgent(stdout io.Writer, name string) error {
	ad, err := agent.Get(name)
	if err != nil {
		return err
	}
	settings := ad.SettingsPath()
	raw, err := readSettings(settings)
	if err != nil {
		return err
	}
	hiveHooks, claudeMem, commented, err := countHooks(raw, name)
	if err != nil {
		fmt.Fprintf(stdout, "%s hooks: cannot read %s: %v\n", name, settings, err)
		return nil
	}
	fmt.Fprintf(stdout, "%s hooks: %d hive hook(s) in %s\n", name, hiveHooks, settings)
	if commented {
		fmt.Fprintf(stdout, "%s: %s has comments; hive install cannot edit it safely\n", name, filepath.Base(settings))
	}
	if hiveHooks > 0 {
		printWarnings(stdout, ad, raw)
	}
	if claudeMem > 0 {
		fmt.Fprintf(stdout, "WARNING: %s has %d claude-mem hook(s); Hive replaces claude-mem, so remove them\n", settings, claudeMem)
	}
	return nil
}

func printWarnings(stdout io.Writer, ad agent.Adapter, raw []byte) {
	if !ad.Capabilities().HasPreTool {
		fmt.Fprintf(stdout, "%s: no pre-tool hook\n", ad.Name())
	}
	if w, ok := ad.(agent.Warner); ok {
		for _, msg := range w.Warnings(raw) {
			fmt.Fprintln(stdout, msg)
		}
	}
}

// readSettings returns a settings file's bytes, or nil when it does not exist.
func readSettings(settings string) ([]byte, error) {
	raw, err := os.ReadFile(settings) //nolint:gosec // G304: the settings path is the user's own file
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return raw, err
}

// countHooks counts the hook commands in a settings file that run Hive for
// agent name, and the ones that run claude-mem. commented reports comments in
// the file, which Inspect reads through.
func countHooks(raw []byte, name string) (hive, claudeMem int, commented bool, err error) {
	if raw == nil {
		return 0, 0, false, nil
	}
	var m struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	commented, err = hookjson.Inspect(raw, &m)
	if err != nil {
		return 0, 0, false, err
	}
	for _, groups := range m.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				switch {
				case agent.IsHookCommand(h.Command, name):
					hive++
				case strings.Contains(h.Command, "claude-mem"):
					claudeMem++
				}
			}
		}
	}
	return hive, claudeMem, commented, nil
}

// scrubberSelfTest checks that one secret is redacted and one ordinary
// command is left alone. The secret is assembled so source scanners skip it.
func scrubberSelfTest() string {
	sc, err := scrub.New(nil)
	if err != nil {
		return "FAILED: " + err.Error()
	}
	secret := "export ANTHROPIC_API_KEY=sk-ant-" + "api03-" + strings.Repeat("A", 24)
	if out, _ := sc.Scrub(secret); strings.Contains(out, "api03") {
		return "FAILED: secret not redacted"
	}
	const plain = "run go test ./..."
	if out, _ := sc.Scrub(plain); out != plain {
		return "FAILED: ordinary command changed"
	}
	return "ok"
}

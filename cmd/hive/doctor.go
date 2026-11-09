package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/scrub"
	"github.com/a6n-ai/hive/internal/store"
)

func doctorCmd(stdout io.Writer) error {
	home := config.Home()
	settings, err := defaultSettings()
	if err != nil {
		return err
	}
	hiveHooks, claudeMem, err := countHooks(settings)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "hooks: %d hive hook(s) in %s\n", hiveHooks, settings)

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
	if claudeMem > 0 {
		fmt.Fprintf(stdout, "WARNING: %s has %d claude-mem hook(s); Hive replaces claude-mem, so remove them\n", settings, claudeMem)
	}
	return nil
}

func defaultSettings() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// countHooks counts the Claude Code hook commands that run Hive and the ones
// that run claude-mem. A missing settings file has none.
func countHooks(settings string) (hive, claudeMem int, err error) {
	raw, err := os.ReadFile(settings)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var m struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, 0, fmt.Errorf("%s: %w", settings, err)
	}
	for _, groups := range m.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				switch {
				case strings.Contains(h.Command, " hook --agent claude-code "):
					hive++
				case strings.Contains(h.Command, "claude-mem"):
					claudeMem++
				}
			}
		}
	}
	return hive, claudeMem, nil
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

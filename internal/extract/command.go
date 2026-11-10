package extract

import (
	"context"
	"regexp"
	"strings"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

// classRules is checked in order; the first match wins.
var classRules = []struct {
	class string
	re    *regexp.Regexp
}{
	{"test", regexp.MustCompile(`\b(?:test|tests|pytest|jest|vitest|rspec)\b`)},
	{"lint", regexp.MustCompile(`\b(?:lint|vet|ruff|eslint|golangci-lint|clippy|tsc\s+--noEmit)\b`)},
	{"build", regexp.MustCompile(`\b(?:build|tsc|compile|bundle)\b|^make\b$`)},
	// Entrypoints only; everyday shell (ls, cat, git status) is never stored.
	{"run", regexp.MustCompile(`^(?:make\s+\S+|(?:npm|pnpm|yarn|bun)\s+run\s+\S+|go\s+run\b|cargo\s+run\b|docker\s+compose\s+up\b)`)},
}

var classLabel = map[string]string{"test": "tests", "lint": "lint", "build": "build", "run": "run"}

// NormalizeCommand collapses whitespace so equivalent commands share a key.
func NormalizeCommand(cmd string) string { return strings.Join(strings.Fields(cmd), " ") }

// ClassifyCommand returns the command's class, or "" when it is not worth
// remembering.
func ClassifyCommand(cmd string) string {
	for _, r := range classRules {
		if r.re.MatchString(cmd) {
			return r.class
		}
	}
	return ""
}

type commandX struct{}

// NewCommand remembers the commands that work in a project.
func NewCommand() Extractor { return commandX{} }

func (commandX) Kinds() []model.EventKind { return []model.EventKind{model.CommandResult} }

func (commandX) Extract(_ context.Context, e *model.Event, tx *store.Tx, _ config.Config) error {
	if code, ok := intVal(e.Payload, "exit_code"); !ok || code != 0 {
		return nil
	}
	cmd := NormalizeCommand(str(e.Payload, "command"))
	class := ClassifyCommand(cmd)
	if class == "" {
		return nil
	}
	m := projectMemory(e, model.Command, cmd, classLabel[class]+": "+cmd, map[string]any{"class": class}, model.Active)
	_, err := tx.UpsertMemory(m, nil, e.ID)
	return err
}

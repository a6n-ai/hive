package extract

import (
	"context"
	"path/filepath"
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

var (
	quoted     = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	segmentSep = regexp.MustCompile(`&&|\|\||[;|&]`)
	envAssign  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// classPrograms are the programs whose own words name a class. Any other
// program (rg, git, sqlite3, echo) only mentions "test" as data.
var classPrograms = map[string]bool{
	"go": true, "cargo": true, "npm": true, "pnpm": true, "yarn": true, "bun": true, "bunx": true, "npx": true,
	"deno": true, "make": true, "mvn": true, "mvnw": true, "gradle": true, "gradlew": true, "dotnet": true,
	"uv": true, "poetry": true, "python": true, "python3": true, "swift": true, "mix": true, "zig": true,
	"docker": true, "turbo": true, "nx": true,
	"pytest": true, "jest": true, "vitest": true, "rspec": true, "eslint": true, "ruff": true,
	"golangci-lint": true, "tsc": true,
}

// commandHeads returns the first words of each segment of a shell line that
// runs a class program, with quoted text removed, so a class word inside an
// argument or a commit message never counts.
func commandHeads(cmd string) []string {
	var heads []string
	for _, seg := range segmentSep.Split(quoted.ReplaceAllString(cmd, "Q"), -1) {
		f := strings.Fields(seg)
		for len(f) > 0 && envAssign.MatchString(f[0]) {
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		f[0] = filepath.Base(f[0])
		if !classPrograms[f[0]] {
			continue
		}
		heads = append(heads, strings.Join(f[:min(len(f), 4)], " "))
	}
	return heads
}

// ClassifyCommand returns the command's class, or "" when it is not worth
// remembering.
func ClassifyCommand(cmd string) string {
	heads := commandHeads(cmd)
	for _, r := range classRules {
		for _, h := range heads {
			if r.re.MatchString(h) {
				return r.class
			}
		}
	}
	return ""
}

// maxCommandLen drops pasted scripts: a real entrypoint command is short.
const maxCommandLen = 200

type commandX struct{}

// NewCommand remembers the commands that work in a project.
func NewCommand() Extractor { return commandX{} }

func (commandX) Kinds() []model.EventKind { return []model.EventKind{model.CommandResult} }

func (commandX) Extract(_ context.Context, e *model.Event, tx *store.Tx, _ config.Config) error {
	if code, ok := intVal(e.Payload, "exit_code"); !ok || code != 0 {
		return nil
	}
	raw := str(e.Payload, "command")
	cmd := NormalizeCommand(raw)
	if strings.ContainsAny(raw, "\n\r") || strings.Contains(raw, "<<") || len(cmd) > maxCommandLen {
		return nil
	}
	class := ClassifyCommand(cmd)
	if class == "" {
		return nil
	}
	m := projectMemory(e, model.Command, cmd, classLabel[class]+": "+cmd, map[string]any{"class": class}, model.Active)
	_, err := tx.UpsertMemory(m, nil, e.ID)
	return err
}

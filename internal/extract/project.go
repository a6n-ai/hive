package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

// manifests maps a file in the project root to the language it implies.
var manifests = []struct{ file, text string }{
	{"go.mod", "Go module"},
	{"package.json", "Node package"},
	{"Cargo.toml", "Rust crate"},
	{"pyproject.toml", "Python project"},
}

var composeFiles = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

type projectX struct{}

// NewProject records what kind of project a session runs in.
func NewProject() Extractor { return projectX{} }

func (projectX) Kinds() []model.EventKind { return []model.EventKind{model.SessionStart} }

func (projectX) Extract(_ context.Context, e *model.Event, tx *store.Tx, _ config.Config) error {
	var langs []string
	for _, m := range manifests {
		if exists(filepath.Join(e.Project, m.file)) {
			langs = append(langs, m.text)
		}
	}
	if len(langs) > 0 {
		if _, err := tx.UpsertMemory(projectMemory(e, model.Project, "language", strings.Join(langs, ", "), nil, model.Active), nil, e.ID); err != nil {
			return err
		}
	}
	for _, f := range composeFiles {
		if exists(filepath.Join(e.Project, f)) {
			_, err := tx.UpsertMemory(projectMemory(e, model.Project, "services", "Docker Compose ("+f+")", nil, model.Active), nil, e.ID)
			return err
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

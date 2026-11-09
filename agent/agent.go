// Package agent is the seam between Hive and a coding agent: how an agent's
// hook payloads become events, what it is told back, and how its hooks are
// installed. Each agent lives in its own subpackage and registers itself.
package agent

import (
	"fmt"

	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/model"
)

// Caps describes what an agent's hooks allow Hive to do.
type Caps struct {
	CanInject, CanBlock, HasPreTool, HasSessionEnd bool
}

type Adapter interface {
	Name() string
	Parse(event string, stdin []byte) (model.Event, error)
	Reply(event string, lines []recall.Line) []byte
	Capabilities() Caps
	Install(settingsPath, hiveBin string) error
	Uninstall(settingsPath string) error
	InstructionsFile() string
	SkillDirs() []string
}

var registry = map[string]Adapter{}

// Register makes an adapter available to Get. Adapters register from init.
func Register(a Adapter) { registry[a.Name()] = a }

func Get(name string) (Adapter, error) {
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("agent: unknown agent %q", name)
	}
	return a, nil
}

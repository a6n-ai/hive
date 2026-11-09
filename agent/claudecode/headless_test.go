package claudecode

import (
	"slices"
	"testing"
)

func TestHeadlessCommandHasNoTools(t *testing.T) {
	cmd := New().Headless("haiku").Command
	i := slices.Index(cmd, "--tools")
	if i < 0 || i+1 >= len(cmd) || cmd[i+1] != "" {
		t.Errorf("command %q: want --tools followed by an empty string", cmd)
	}
	if !slices.Contains(cmd, "--strict-mcp-config") {
		t.Errorf("command %q: want --strict-mcp-config", cmd)
	}
	if slices.Contains(cmd, "--bare") {
		t.Errorf("command %q must not use --bare (it skips keychain auth)", cmd)
	}
}

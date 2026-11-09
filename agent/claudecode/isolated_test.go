package claudecode

import (
	"slices"
	"testing"
)

func TestHeadlessCommandIsolated(t *testing.T) {
	cmd := New().Headless("haiku").Command
	if !slices.Contains(cmd, "--no-session-persistence") {
		t.Errorf("command %q: want --no-session-persistence so review runs leave no transcript", cmd)
	}
	if i := slices.Index(cmd, "--setting-sources"); i < 0 || i+1 >= len(cmd) || cmd[i+1] != "" {
		t.Errorf("command %q: want --setting-sources followed by an empty string", cmd)
	}
	if i := slices.Index(cmd, "--tools"); i < 0 || i+1 >= len(cmd) || cmd[i+1] != "" {
		t.Errorf("command %q: want --tools followed by an empty string", cmd)
	}
	if !slices.Contains(cmd, "--strict-mcp-config") || slices.Contains(cmd, "--bare") {
		t.Errorf("command %q: want --strict-mcp-config and no --bare", cmd)
	}
}

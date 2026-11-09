package claudecode

import (
	"reflect"
	"testing"
)

func TestNormalizeTool(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		input    map[string]any
		failed   bool
		errText  string
		wantTool string
		wantOut  map[string]any
	}{
		{
			name:     "success Bash",
			tool:     "Bash",
			input:    map[string]any{"command": "go test ./..."},
			wantTool: "shell",
			wantOut:  map[string]any{"command": "go test ./...", "exit_code": 0},
		},
		{
			name:     "failed Bash with exit code",
			tool:     "Bash",
			input:    map[string]any{"command": "make"},
			failed:   true,
			errText:  "Exit code 2\nboom",
			wantTool: "shell",
			wantOut:  map[string]any{"command": "make", "exit_code": 2, "error": "boom"},
		},
		{
			name:     "failed Bash without exit code line",
			tool:     "Bash",
			input:    map[string]any{"command": "make"},
			failed:   true,
			errText:  "killed",
			wantTool: "shell",
			wantOut:  map[string]any{"command": "make", "error": "killed"},
		},
		{
			name:     "failed Read",
			tool:     "Read",
			input:    map[string]any{"file_path": "/tmp/x.go"},
			failed:   true,
			errText:  "File does not exist.",
			wantTool: "read",
			wantOut:  map[string]any{"path": "/tmp/x.go", "error": "File does not exist."},
		},
		{
			name:     "Skill",
			tool:     "Skill",
			input:    map[string]any{"skill": "superpowers:brainstorming"},
			wantTool: "skill",
			wantOut:  map[string]any{"skill": "superpowers:brainstorming"},
		},
		{
			name:     "unknown tool passes through",
			tool:     "WebFetch",
			input:    map[string]any{"url": "https://example.com"},
			wantTool: "WebFetch",
			wantOut:  map[string]any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTool, gotOut := NormalizeTool(tt.tool, tt.input, tt.failed, tt.errText)
			if gotTool != tt.wantTool {
				t.Errorf("tool = %q, want %q", gotTool, tt.wantTool)
			}
			if !reflect.DeepEqual(gotOut, tt.wantOut) {
				t.Errorf("payload = %#v, want %#v", gotOut, tt.wantOut)
			}
		})
	}
}

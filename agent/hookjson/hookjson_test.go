package hookjson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallNestedWithExtraFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","hooks":{"BeforeAgent":[{"matcher":"x","hooks":[{"type":"command","command":"other-tool"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{{Event: "BeforeAgent", Command: "/bin/hive hook --agent gemini BeforeAgent", Extra: map[string]any{"matcher": "*", "name": "hive"}}}
	if err := Install(path, entries, Owns("gemini")); err != nil {
		t.Fatal(err)
	}
	m := readHooks(t, path)
	if m["theme"] != "dark" {
		t.Fatalf("unknown key lost: %v", m)
	}
	groups := m["hooks"].(map[string]any)["BeforeAgent"].([]any)
	if len(groups) != 2 {
		t.Fatalf("want the other tool's group kept and one hive group added, got %d groups", len(groups))
	}
	hive := groups[1].(map[string]any)
	if hive["matcher"] != "*" {
		t.Fatalf("group matcher = %v, want *", hive["matcher"])
	}
	hook := hive["hooks"].([]any)[0].(map[string]any)
	if hook["name"] != "hive" || hook["type"] != "command" {
		t.Fatalf("hook = %v, want name hive and type command", hook)
	}
	if err := Uninstall(path, Owns("gemini")); err != nil {
		t.Fatal(err)
	}
	left := readHooks(t, path)["hooks"].(map[string]any)["BeforeAgent"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["matcher"] != "x" {
		t.Fatalf("uninstall should keep only the other tool's group, got %v", left)
	}
}

func TestOwnsMatcherAcrossBinaries(t *testing.T) {
	owns := Owns("codex")
	if !owns("/old/place/hive hook --agent codex SessionStart") {
		t.Fatal("codex hook from another binary path not recognised")
	}
	if owns("/bin/hive hook --agent gemini SessionStart") {
		t.Fatal("gemini hook recognised as codex")
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	one := []Entry{{Event: "Stop", Command: "/old/hive hook --agent codex Stop"}}
	two := []Entry{{Event: "Stop", Command: "/new/hive hook --agent codex Stop"}}
	if err := Install(path, one, owns); err != nil {
		t.Fatal(err)
	}
	if err := Install(path, two, owns); err != nil {
		t.Fatal(err)
	}
	groups := readHooks(t, path)["hooks"].(map[string]any)["Stop"].([]any)
	if len(groups) != 1 {
		t.Fatalf("reinstall from a new binary left %d groups, want 1", len(groups))
	}
	if cmd := groups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]; cmd != "/new/hive hook --agent codex Stop" {
		t.Fatalf("command = %v, want the new binary", cmd)
	}
}

func TestInstallRefusesNonArrayEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Install(path, []Entry{{Event: "Stop", Command: "c"}}, Owns("codex"))
	if err == nil {
		t.Fatal("want an error for a non-array event")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != `{"hooks":{"Stop":{}}}` {
		t.Fatalf("file changed on refusal: %s", raw)
	}
}

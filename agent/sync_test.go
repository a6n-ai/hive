package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	begin = "<!-- inscribe:begin -->"
	end   = "<!-- inscribe:end -->"
)

func TestWriteBlockAppendsCreatesReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := WriteBlock(path, "one"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != begin+"\none\n"+end+"\n" {
		t.Fatalf("created file = %q", got)
	}
	writeFile(t, path, "# Title\n\nuser text\n")
	if err := WriteBlock(path, "one"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "# Title\n\nuser text\n\n"+begin+"\none\n"+end+"\n" {
		t.Fatalf("appended = %q", got)
	}
	if err := WriteBlock(path, "two"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "# Title\n\nuser text\n\n"+begin+"\ntwo\n"+end+"\n" {
		t.Fatalf("replaced = %q", got)
	}
}

func TestWriteBlockKeepsCRLFAndUserText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	writeFile(t, path, "# T\r\n\r\nuser\r\n"+begin+"\r\nold\r\n"+end+"\r\ntail\r\n")
	if err := WriteBlock(path, "new"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "# T\r\n\r\nuser\r\n"+begin+"\r\nnew\r\n"+end+"\r\ntail\r\n" {
		t.Fatalf("replaced CRLF = %q", got)
	}
	writeFile(t, path, "# T\r\n")
	if err := WriteBlock(path, "new"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "# T\r\n\r\n"+begin+"\r\nnew\r\n"+end+"\r\n" {
		t.Fatalf("appended CRLF = %q", got)
	}
}

func TestWriteBlockRejectsMalformedMarkers(t *testing.T) {
	cases := map[string]string{
		"begin without end": "x\n" + begin + "\nold\n",
		"end without begin": "x\n" + end + "\n",
		"two begins":        begin + "\na\n" + begin + "\nb\n" + end + "\n",
		"end before begin":  end + "\n" + begin + "\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "CLAUDE.md")
			writeFile(t, path, content)
			if err := WriteBlock(path, "new"); err == nil {
				t.Fatal("malformed markers accepted")
			}
			if got := readFile(t, path); got != content {
				t.Fatalf("file changed on refusal: %q", got)
			}
		})
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWriteBlockRejectsMarkerInBody(t *testing.T) {
	for _, body := range []string{"one\n" + end + "\ntwo", "one\n" + begin + "\n", "x\r\n" + end} {
		path := filepath.Join(t.TempDir(), "CLAUDE.md")
		writeFile(t, path, "# T\n")
		if err := WriteBlock(path, body); err == nil {
			t.Fatalf("body with a marker line accepted: %q", body)
		}
		if got := readFile(t, path); got != "# T\n" {
			t.Fatalf("file changed on refusal: %q", got)
		}
	}
}

func TestWriteBlockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.md")
	writeFile(t, target, "outside text\n")
	path := filepath.Join(dir, "project", "CLAUDE.md")
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	err := WriteBlock(path, "one")
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("symlink write error = %v, want one naming %s", err, path)
	}
	if got := readFile(t, target); got != "outside text\n" {
		t.Fatalf("target changed through the symlink: %q", got)
	}
}

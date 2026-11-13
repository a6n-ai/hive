package claudecode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeSessionFile(t *testing.T, dir string, pid int, session string) {
	t.Helper()
	b := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle"}`, pid, session)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestLiveSessionsReadsLivePIDs(t *testing.T) {
	dir := t.TempDir()
	writeSessionFile(t, dir, os.Getpid(), "alive")
	writeSessionFile(t, dir, deadPID(t), "crashed")
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	live, ok := liveSessions(dir)
	if !ok || !live["alive"] || live["crashed"] || len(live) != 1 {
		t.Fatalf("live = %v, ok %v; want only alive", live, ok)
	}
}

func TestLiveSessionsUnknownWithoutDir(t *testing.T) {
	if _, ok := liveSessions(filepath.Join(t.TempDir(), "missing")); ok {
		t.Fatal("a missing sessions directory reported as known")
	}
}

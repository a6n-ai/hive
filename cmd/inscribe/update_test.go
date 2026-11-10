package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrewManaged(t *testing.T) {
	for path, want := range map[string]bool{
		"/opt/homebrew/Caskroom/inscribe/0.1.0/inscribe":          true,
		"/usr/local/Caskroom/inscribe/0.1.0/inscribe":             true,
		"/home/linuxbrew/.linuxbrew/Caskroom/inscribe/x/inscribe": true,
		"/Users/me/.local/bin/inscribe":                           false,
		"/Users/me/go/bin/inscribe":                               false,
	} {
		if got := brewManaged(path); got != want {
			t.Errorf("brewManaged(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestUpdateRunsInstallScriptForThisBinary(t *testing.T) {
	home := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "env.log")
	script := filepath.Join(t.TempDir(), "install.sh")
	body := "#!/bin/sh\necho \"dir=$INSCRIBE_INSTALL_DIR\" >> \"$FAKE_LOG\"\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runCLI(t, home, "", []string{"INSCRIBE_INSTALL_SCRIPT_URL=file://" + script, "FAKE_LOG=" + logPath}, "update")
	if code != 0 {
		t.Fatalf("update: exit %d\n%s\n%s", code, out, errOut)
	}
	real, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(readText(t, logPath)), "dir="+filepath.Dir(real); got != want {
		t.Errorf("install script got %q, want %q", got, want)
	}
}

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const installScriptURL = "https://raw.githubusercontent.com/a6n-ai/inscribe/main/install.sh"

// updateCmd replaces this binary with the latest release by running install.sh
// for the binary's own directory, so download, checksum and atomic replace live
// in one place. A Homebrew install is left to brew.
func updateCmd(stdout, stderr io.Writer) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	if brewManaged(bin) {
		_, err := fmt.Fprintln(stdout, "inscribe was installed with Homebrew; run: brew upgrade inscribe")
		return err
	}
	url := installScriptURL
	if u := os.Getenv("INSCRIBE_INSTALL_SCRIPT_URL"); u != "" {
		url = u
	}
	// The URL and directory reach the shell as variables, never as script text.
	cmd := exec.Command("sh", "-c", `curl --proto '=https,file' --tlsv1.2 -fsSL "$INSCRIBE_SCRIPT" | sh`) //nolint:gosec // G204: fixed script text
	cmd.Env = append(os.Environ(), "INSCRIBE_SCRIPT="+url, "INSCRIBE_INSTALL_DIR="+filepath.Dir(bin))
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// brewManaged reports whether path is inside a Homebrew Caskroom.
func brewManaged(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/Caskroom/")
}

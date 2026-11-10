package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeUname makes install.sh see the given OS and architecture.
func fakeUname(t *testing.T, osName, arch string) string {
	t.Helper()
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in -s) echo %s ;; -m) echo %s ;; esac\n", osName, arch)
	if err := os.WriteFile(filepath.Join(bin, "uname"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: test fake must be executable
		t.Fatal(err)
	}
	return bin
}

// makeRelease writes a release archive and checksums.txt under base/tag, as GitHub serves them.
func makeRelease(t *testing.T, base, tag, osName, arch string, checksum string) string {
	t.Helper()
	name := fmt.Sprintf("hive_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), osName, arch)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho fake-hive\n")
	if err := tw.WriteHeader(&tar.Header{Name: "hive", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, tag)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := checksum
	if sum == "" {
		h := sha256.Sum256(buf.Bytes())
		sum = hex.EncodeToString(h[:])
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sum+"  "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

// runInstallSh runs install.sh with a temp HOME and the given overrides; it returns output and exit code.
func runInstallSh(t *testing.T, env ...string) (string, int) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command("sh", "../../install.sh")
	cmd.Env = append(append(os.Environ(), "HOME="+home), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

func installEnv(t *testing.T, osName, arch string) (base, installDir string, env []string) {
	t.Helper()
	base, installDir = t.TempDir(), filepath.Join(t.TempDir(), "bin")
	path := fakeUname(t, osName, arch) + string(os.PathListSeparator) + os.Getenv("PATH")
	return base, installDir, []string{
		"PATH=" + path,
		"HIVE_VERSION=v1.2.3",
		"HIVE_INSTALL_BASE_URL=file://" + base,
		"HIVE_INSTALL_DIR=" + installDir,
	}
}

func TestInstallShInstallsBinary(t *testing.T) {
	base, installDir, env := installEnv(t, "Linux", "x86_64")
	makeRelease(t, base, "v1.2.3", "linux", "amd64", "")
	out, code := runInstallSh(t, env...)
	if code != 0 {
		t.Fatalf("install.sh exit %d:\n%s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(installDir, "hive"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fake-hive") {
		t.Fatalf("installed binary is not the release binary: %q", b)
	}
	info, err := os.Stat(filepath.Join(installDir, "hive"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("installed hive is not executable: %v", info.Mode())
	}
	if !strings.Contains(out, "hive setup") {
		t.Errorf("output %q does not say to run hive setup", out)
	}
}

func TestInstallShChecksumMismatch(t *testing.T) {
	base, installDir, env := installEnv(t, "Linux", "x86_64")
	makeRelease(t, base, "v1.2.3", "linux", "amd64", strings.Repeat("0", 64))
	out, code := runInstallSh(t, env...)
	if code == 0 {
		t.Fatalf("checksum mismatch exited 0:\n%s", out)
	}
	if !strings.Contains(out, "checksum") {
		t.Errorf("output %q does not name the checksum", out)
	}
	if _, err := os.Stat(filepath.Join(installDir, "hive")); err == nil {
		t.Error("hive was installed despite the checksum mismatch")
	}
}

func TestInstallShUnsupportedArch(t *testing.T) {
	base, installDir, env := installEnv(t, "Linux", "mips")
	makeRelease(t, base, "v1.2.3", "linux", "amd64", "")
	out, code := runInstallSh(t, env...)
	if code == 0 {
		t.Fatalf("unsupported arch exited 0:\n%s", out)
	}
	if !strings.Contains(out, "mips") {
		t.Errorf("output %q does not name the architecture", out)
	}
	if _, err := os.Stat(filepath.Join(installDir, "hive")); err == nil {
		t.Error("hive was installed for an unsupported architecture")
	}
}

func TestInstallShReplacesAtomically(t *testing.T) {
	base, installDir, env := installEnv(t, "Linux", "x86_64")
	makeRelease(t, base, "v1.2.3", "linux", "amd64", "")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(installDir, "hive")
	if err := os.WriteFile(old, []byte("old-hive"), 0o755); err != nil { //nolint:gosec // G306: test fake must be executable
		t.Fatal(err)
	}
	before, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	if out, code := runInstallSh(t, env...); code != 0 {
		t.Fatalf("install.sh exit %d:\n%s", code, out)
	}
	after, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("installed hive reuses the old inode; a running hive would be overwritten in place")
	}
	entries, err := os.ReadDir(installDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "hive" {
		t.Errorf("install dir entries = %v, want only hive", entries)
	}
}

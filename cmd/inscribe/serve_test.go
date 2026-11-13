package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/a6n-ai/inscribe"
)

const testHost = "127.0.0.1:7331"

func serveReq(h http.Handler, method, target, form, host string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(form))
	req.Host = host
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func writeConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServeRefusesNonLoopbackAddr(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7331", "localhost:7331", "example.com:7331", ":7331", "127.0.0.1:x"} {
		var out bytes.Buffer
		if err := serveCmd([]string{"--addr", addr}, &out); err == nil {
			t.Fatalf("serveCmd(%q) returned nil, want an error", addr)
		}
	}
}

func TestServeRefusesForeignHost(t *testing.T) {
	h := newServeHandler(t.TempDir(), 7331)
	if rec := serveReq(h, "GET", "/", "", "evil.test:7331", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host status = %d, want 403", rec.Code)
	}
	if rec := serveReq(h, "GET", "/", "", "127.0.0.1:9999", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong port status = %d, want 403", rec.Code)
	}
	if rec := serveReq(h, "GET", "/", "", testHost, nil); rec.Code != http.StatusOK {
		t.Fatalf("loopback host status = %d, want 200", rec.Code)
	}
}

func TestServeShareKeepsOtherKeys(t *testing.T) {
	home := t.TempDir()
	path := writeConfig(t, home, "[recall]\nsession_budget = 7\n\n[consolidate]\nauto = false\n")
	h := newServeHandler(home, 7331)
	if rec := serveReq(h, "POST", "/share", "analytics=1", testHost, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		t.Fatal(err)
	}
	share, _ := doc["share"].(map[string]any)
	recall, _ := doc["recall"].(map[string]any)
	if share["analytics"] != true {
		t.Fatalf("share.analytics = %v, want true", share["analytics"])
	}
	if recall["session_budget"] != int64(7) {
		t.Fatalf("recall.session_budget = %v, want 7", recall["session_budget"])
	}
}

func TestServeShareMissingFile(t *testing.T) {
	home := t.TempDir()
	h := newServeHandler(home, 7331)
	if rec := serveReq(h, "POST", "/share", "analytics=1", testHost, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	got, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[share]\nanalytics = true\n" {
		t.Fatalf("config.toml = %q", got)
	}
	info, err := os.Stat(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestServeShareBadToml(t *testing.T) {
	home := t.TempDir()
	bad := "[share\nanalytics = \n"
	path := writeConfig(t, home, bad)
	h := newServeHandler(home, 7331)
	if rec := serveReq(h, "POST", "/share", "analytics=1", testHost, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != bad {
		t.Fatalf("config.toml changed to %q", got)
	}
}

func TestServeShareRejectsBadInput(t *testing.T) {
	home := t.TempDir()
	h := newServeHandler(home, 7331)
	if rec := serveReq(h, "POST", "/share", "analytics=yes", testHost, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad value status = %d, want 400", rec.Code)
	}
	if rec := serveReq(h, "POST", "/share", "analytics=1", testHost, map[string]string{"Origin": "https://evil.test"}); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d, want 403", rec.Code)
	}
	if rec := serveReq(h, "GET", "/share", "", testHost, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /share status = %d, want 405", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml written after rejected requests: %v", err)
	}
}

func TestServeHidesSecrets(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[share]\nanalytics = true\nurl = \"https://tok@host.example/x?q=secret\"\n")
	h, err := inscribe.Open(inscribe.Options{Home: home, Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := h.InstallID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	rec := serveReq(newServeHandler(home, 7331), "GET", "/", "", testHost, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "host.example") {
		t.Fatalf("page does not show the host")
	}
	for _, secret := range []string{"tok", "secret", "/x", id} {
		if strings.Contains(body, secret) {
			t.Fatalf("page leaks %q", secret)
		}
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/model"
)

func TestStatsIngestCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INSCRIBE_HOME", home)
	tp := filepath.Join(home, "t.jsonl")
	if err := os.WriteFile(tp, []byte(`{"type":"assistant","timestamp":"2026-11-14T10:00:00Z","message":{"id":"m","model":"x","usage":{"input_tokens":1,"output_tokens":2}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := inscribe.Open(inscribe.Options{Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	ev := inscribe.Event{Agent: "claude-code", Session: "s1", Project: "p", Kind: model.SessionEnd, TS: 1, TranscriptPath: tp}
	if err := h.Capture(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()

	out, errOut, code := runCLI(t, home, "", nil, "stats", "ingest", "--catch-up")
	if code != 0 || out != "ingested 1 session(s)\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	_, errOut, code = runCLI(t, home, "", nil, "stats")
	if code != 2 {
		t.Fatalf("bare stats code=%d err=%q", code, errOut)
	}
}

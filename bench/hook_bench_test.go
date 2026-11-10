// Package bench measures the hook binary's latency on a large database. The
// benchmarks are skipped unless INSCRIBE_BENCH=1, because seeding takes minutes.
package bench

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

const (
	seedEvents   = 1_000_000
	seedMemories = 50_000
	seedBatch    = 10_000
	seedProjects = 100
)

var promptPayload = []byte(`{"session_id":"bench-live","cwd":"/work/proj-0","hook_event_name":"UserPromptSubmit","prompt":"how do we deploy the service to staging"}`)

func BenchmarkHookPrompt(b *testing.B) {
	home, bin := setup(b)
	b.ResetTimer()
	measure(b, bin, home)
}

// BenchmarkHookPromptLocked holds the write lock from another connection, so
// the hook must wait out busy_timeout and spool its event.
func BenchmarkHookPromptLocked(b *testing.B) {
	home, bin := setup(b)
	st, err := store.Open(filepath.Join(home, "inscribe.db"), false)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- st.Write(context.Background(), func(*store.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer func() {
		close(release)
		if err := <-done; err != nil {
			b.Error(err)
		}
	}()
	b.ResetTimer()
	measure(b, bin, home)
}

// setup seeds (once, cached under .cache) and builds the hook binary.
func setup(b *testing.B) (home, bin string) {
	b.Helper()
	if os.Getenv("INSCRIBE_BENCH") != "1" {
		b.Skip("set INSCRIBE_BENCH=1 to run the hook benchmarks")
	}
	home = filepath.Join(".cache", "seed-home")
	if err := seed(home); err != nil {
		b.Fatal(err)
	}
	bin = filepath.Join(b.TempDir(), "inscribe")
	build := exec.Command("go", "build", "-o", bin, "../cmd/inscribe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		b.Fatalf("build: %v\n%s", err, out)
	}
	return home, bin
}

// measure runs the hook once per sample and reports the p95 latency.
func measure(b *testing.B, bin, home string) {
	durs := make([]time.Duration, 0, b.N)
	for range b.N {
		cmd := exec.Command(bin, "hook", "--agent", "claude-code", "UserPromptSubmit")
		cmd.Env = append(os.Environ(), "INSCRIBE_HOME="+home)
		cmd.Stdin = bytes.NewReader(promptPayload)
		var out bytes.Buffer
		cmd.Stdout = &out
		start := time.Now()
		if err := cmd.Run(); err != nil {
			b.Fatal(err)
		}
		durs = append(durs, time.Since(start))
		if !strings.Contains(out.String(), "staging") {
			b.Fatalf("hook injected no memory: %q", out.String())
		}
	}
	b.ReportMetric(float64(p95(durs))/float64(time.Millisecond), "p95-ms")
}

func p95(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[int(math.Ceil(0.95*float64(len(s))))-1]
}

func project(i int) string { return fmt.Sprintf("/work/proj-%d", i%seedProjects) }

// seed fills home with seedEvents events and seedMemories memories, in
// transactions of seedBatch. A marker file skips seeding on later runs.
func seed(home string) error {
	marker := filepath.Join(home, "seeded")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(home, "inscribe.db"), false)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	for base := 0; base < seedEvents; base += seedBatch {
		if err := st.Write(ctx, func(tx *store.Tx) error {
			for i := base; i < base+seedBatch; i++ {
				e := model.Event{TS: now - int64(seedEvents-i), Agent: "claude-code", Session: fmt.Sprintf("s-%d", i%5000),
					Project: project(i), Kind: model.PostTool, Tool: "shell", Digest: fmt.Sprintf("seed-event-%d", i),
					Payload: map[string]any{"command": "go test ./...", "exit_code": 0}}
				if i%3 == 0 {
					e = model.Event{TS: e.TS, Agent: e.Agent, Session: e.Session, Project: e.Project, Kind: model.Prompt,
						PromptText: "fix the failing login test", Digest: e.Digest, Payload: map[string]any{}}
				}
				if _, err := tx.InsertEvent(&e); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for base := 0; base < seedMemories; base += seedBatch {
		if err := st.Write(ctx, func(tx *store.Tx) error {
			for i := base; i < base+seedBatch && i < seedMemories; i++ {
				m := &model.Memory{Type: model.Command, ScopeKind: model.ProjectScope, Scope: project(i), Key: fmt.Sprintf("cmd-%d", i),
					Body: map[string]any{}, Text: fmt.Sprintf("tests: make target-%d check", i), Confidence: 0.8,
					Status: model.Active, FirstSeen: now, LastSeen: now}
				if _, err := tx.UpsertMemory(m, nil, 0); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	target := &model.Memory{Type: model.Note, ScopeKind: model.ProjectScope, Scope: project(0), Key: "deploy-staging",
		Body: map[string]any{}, Text: "deploy the service to staging with make deploy", Confidence: 0.9,
		Status: model.Active, FirstSeen: now, LastSeen: now}
	if err := st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.UpsertMemory(target, nil, 0)
		return err
	}); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, 0o600)
}

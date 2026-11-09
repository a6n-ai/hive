package consolidate_test

import (
	"context"
	"testing"
	"time"

	"github.com/a6n-ai/hive/agent"
	"github.com/a6n-ai/hive/internal/consolidate"
)

// TestHeadlessKillsHungChild: a grandchild that keeps stdout open must not hold
// the runner past its timeout.
func TestHeadlessKillsHungChild(t *testing.T) {
	spec := agent.HeadlessSpec{
		Command:     []string{"sh", "-c", "sleep 30 & echo '{}'"},
		ParseOutput: func([]byte) (string, error) { return "", nil },
	}
	start := time.Now()
	_, _ = consolidate.Headless{Spec: spec, Timeout: time.Second}.Run(context.Background(), "prompt")
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Errorf("runner returned after %v, want within timeout + 6 s", elapsed)
	}
}

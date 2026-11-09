package consolidate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/a6n-ai/hive/agent"
)

const (
	defaultTimeout = 120 * time.Second
	maxOutput      = 1 << 20
)

var errOutputTooLarge = errors.New("consolidate: agent output exceeds 1 MB")

// Runner sends one consolidation prompt to an agent and returns the reply text.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// Headless runs an agent's one-shot command with the prompt on stdin. Its
// environment carries HIVE_DISABLED=1 so the child's own hooks record nothing.
type Headless struct {
	Spec    agent.HeadlessSpec
	Timeout time.Duration
}

func (h Headless) Run(ctx context.Context, prompt string) (string, error) {
	if len(h.Spec.Command) == 0 {
		return "", errors.New("consolidate: no headless command")
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out capped
	cmd := exec.CommandContext(ctx, h.Spec.Command[0], h.Spec.Command[1:]...) //nolint:gosec // G204: the command comes from an adapter, not from input
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = &out
	cmd.Env = append(os.Environ(), "HIVE_DISABLED=1")
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("consolidate: agent run: %w", err)
	}
	if out.over {
		return "", errOutputTooLarge
	}
	return h.Spec.ParseOutput(out.buf.Bytes())
}

// capped keeps the first maxOutput bytes written and notes whether more came.
type capped struct {
	buf  bytes.Buffer
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := maxOutput - c.buf.Len()
	if len(p) > room {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.over = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

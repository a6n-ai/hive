package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/recall"
)

// Snapshot is the one JSON body an opted-in install posts after a train.
// It carries no prompt, memory text, path, command, grade, or memory id.
type Snapshot struct {
	InstallID string          `json:"install_id"`
	Version   string          `json:"version"`
	LabelRule string          `json:"label_rule"`
	StepRule  string          `json:"step_rule"`
	Seen      int             `json:"seen"`
	Hits      int             `json:"hits"`
	Misses    int             `json:"misses"`
	Weights   SnapshotWeights `json:"weights"`
}

// SnapshotWeights omits text, which is always 1.
type SnapshotWeights struct {
	Key        float64 `json:"key"`
	Body       float64 `json:"body"`
	Type       float64 `json:"type"`
	Scope      float64 `json:"scope"`
	Agent      float64 `json:"agent"`
	Command    float64 `json:"command"`
	Trajectory float64 `json:"trajectory"`
}

// noRedirect keeps a 307 or 308 from re-POSTing the snapshot to a host the user never configured.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// PostSnapshot sends s as one JSON POST. A non-2xx reply is an error.
func PostSnapshot(ctx context.Context, endpoint string, s Snapshot) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := noRedirect.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("share: POST %s: %s", endpoint, resp.Status)
	}
	return nil
}

// shareSnapshot posts this train's weights when sharing is on. It runs after the
// train commits and logs failures; it never fails the train.
func shareSnapshot(h *inscribe.Inscribe, home string, w recall.Weights) {
	cfg, _, err := config.Load(home, "")
	if err != nil {
		logLine(home, "share: %v", err)
		return
	}
	if !cfg.Share.Analytics {
		return
	}
	id, err := h.InstallID(context.Background())
	if err != nil {
		logLine(home, "share: install id: %v", err)
		return
	}
	if w.Seen <= 0 {
		return
	}
	if !shareURLOK(cfg.Share.URL) {
		logLine(home, "share: url %q is not an absolute http or https URL", cfg.Share.URL)
		return
	}
	s := Snapshot{InstallID: id, Version: version, LabelRule: recall.LabelRule, StepRule: recall.StepRule,
		Seen: w.Seen, Hits: w.Hits, Misses: w.Misses,
		Weights: SnapshotWeights{Key: w.Key, Body: w.Body, Type: w.Type, Scope: w.Scope, Agent: w.Agent, Command: w.Command, Trajectory: w.Trajectory}}
	if err := PostSnapshot(context.Background(), cfg.Share.URL, s); err != nil {
		logLine(home, "%v", err)
	}
}

func shareURLOK(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Combine turns per-install snapshots into release weights. Snapshots from other
// rules and with no install id are ignored, and the latest file per install wins.
// A field whose weighted mean sits more than 0.25 from the median keeps prior.
func Combine(prior recall.Weights, snaps []Snapshot, mtimes []time.Time) recall.Weights {
	latest := map[string]int{}
	for i, s := range snaps {
		if s.LabelRule != recall.LabelRule || s.StepRule != recall.StepRule || s.InstallID == "" {
			continue
		}
		if j, ok := latest[s.InstallID]; !ok || mtimes[i].After(mtimes[j]) {
			latest[s.InstallID] = i
		}
	}
	picks := make([]Snapshot, 0, len(latest))
	for _, i := range latest {
		picks = append(picks, snaps[i])
	}

	field := func(get func(SnapshotWeights) float64, fallback float64) float64 {
		vals := make([]float64, 0, len(picks))
		var sum, total float64
		for _, s := range picks {
			w := float64(max(0, min(s.Seen, 500)))
			vals = append(vals, get(s.Weights))
			sum += get(s.Weights) * w
			total += w
		}
		if len(vals) == 0 || total == 0 {
			return fallback
		}
		mean := sum / total
		slices.Sort(vals)
		n := len(vals)
		median := vals[n/2]
		if n%2 == 0 {
			median = (vals[n/2-1] + vals[n/2]) / 2
		}
		if math.Abs(mean-median) > 0.25 {
			return fallback
		}
		return mean
	}

	out := prior
	out.Text = 1
	out.Key = field(func(w SnapshotWeights) float64 { return w.Key }, prior.Key)
	out.Body = field(func(w SnapshotWeights) float64 { return w.Body }, prior.Body)
	out.Type = field(func(w SnapshotWeights) float64 { return w.Type }, prior.Type)
	out.Scope = field(func(w SnapshotWeights) float64 { return w.Scope }, prior.Scope)
	out.Agent = field(func(w SnapshotWeights) float64 { return w.Agent }, prior.Agent)
	out.Command = field(func(w SnapshotWeights) float64 { return w.Command }, prior.Command)
	out.Trajectory = field(func(w SnapshotWeights) float64 { return w.Trajectory }, prior.Trajectory)
	return out
}

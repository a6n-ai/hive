package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/recall"
	"github.com/a6n-ai/inscribe/internal/store"
)

func TestPostSnapshotBody(t *testing.T) {
	var body []byte
	var ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctype = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()
	s := Snapshot{InstallID: "abc", Version: "v1", LabelRule: recall.LabelRule, StepRule: recall.StepRule, Seen: 3,
		Weights: SnapshotWeights{Key: 1, Body: 0.5, Type: 0.4, Scope: 1, Agent: 0.5, Command: 1, Trajectory: 1}}
	if err := PostSnapshot(context.Background(), srv.URL, s); err != nil {
		t.Fatal(err)
	}
	if ctype != "application/json" {
		t.Fatalf("Content-Type = %q", ctype)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"install_id", "version", "label_rule", "step_rule", "seen", "hits", "misses", "weights"} {
		if _, ok := got[k]; !ok {
			t.Errorf("body missing %q: %s", k, body)
		}
	}
	w, _ := got["weights"].(map[string]any)
	if len(w) != 7 {
		t.Errorf("weights has %d keys, want 7: %s", len(w), body)
	}
	for _, k := range []string{"text", "memory_id"} {
		if strings.Contains(string(body), `"`+k+`"`) {
			t.Errorf("body carries %q: %s", k, body)
		}
	}
}

func TestPostSnapshotTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(4 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	if err := PostSnapshot(context.Background(), srv.URL, Snapshot{}); err == nil {
		t.Fatal("want timeout error")
	}
	if d := time.Since(start); d > 3500*time.Millisecond {
		t.Fatalf("returned after %v, want about 3s", d)
	}
}

func TestPostSnapshotNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadRequest)
	}))
	defer srv.Close()
	if err := PostSnapshot(context.Background(), srv.URL, Snapshot{}); err == nil {
		t.Fatal("want error for 400")
	}
}

func TestPostSnapshotDoesNotFollowRedirects(t *testing.T) {
	var hits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if err := PostSnapshot(context.Background(), srv.URL, Snapshot{}); err == nil {
		t.Fatal("want error for a 307")
	}
	if hits != 0 {
		t.Fatalf("redirect target got %d requests, want 0", hits)
	}
}

func shareFixture(t *testing.T, cfg string) (*inscribe.Inscribe, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("INSCRIBE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := inscribe.Open(inscribe.Options{Home: home, Caller: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, home
}

func TestShareSnapshotGates(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	seen := recall.DefaultWeights()
	seen.Seen = 5
	cases := []struct {
		name string
		cfg  string
		w    recall.Weights
		want int
	}{
		{"analytics off", "[share]\nanalytics = false\nurl = \"" + srv.URL + "\"\n", seen, 0},
		{"nothing seen", "[share]\nanalytics = true\nurl = \"" + srv.URL + "\"\n", recall.DefaultWeights(), 0},
		{"unusable url", "[share]\nanalytics = true\nurl = \"ftp://x\"\n", seen, 0},
		{"all gates open", "[share]\nanalytics = true\nurl = \"" + srv.URL + "\"\n", seen, 1},
	}
	for _, c := range cases {
		hits = 0
		h, home := shareFixture(t, c.cfg)
		shareSnapshot(h, home, c.w)
		if hits != c.want {
			t.Errorf("%s: %d requests, want %d", c.name, hits, c.want)
		}
	}
}

func TestScoreTrainSharesOnlyWhenWeightsSaved(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	_, home := shareFixture(t, "[share]\nanalytics = true\nurl = \""+srv.URL+"\"\n")
	st, err := store.Open(filepath.Join(home, "inscribe.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	w := recall.DefaultWeights()
	w.Seen = 5
	if err := recall.SaveWeights(context.Background(), st, w); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if err := scoreCmd([]string{"train"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("train that wrote nothing posted %d times", hits)
	}
}

func snap(id string, seen int, rule string, command float64) Snapshot {
	return Snapshot{InstallID: id, LabelRule: recall.LabelRule, StepRule: rule, Seen: seen,
		Weights: SnapshotWeights{Key: 1, Body: 1, Type: 1, Scope: 1, Agent: 1, Command: command, Trajectory: 1}}
}

func TestCombineClampsNegativeSeen(t *testing.T) {
	prior := recall.DefaultWeights()
	a, b := snap("a", -1000, recall.StepRule, 0.5), snap("b", 100, recall.StepRule, 0.6)
	got := Combine(prior, []Snapshot{a, b}, []time.Time{time.Now(), time.Now()})
	if math.Abs(got.Command-0.6) > 1e-9 {
		t.Fatalf("Command = %v, want 0.6 (negative seen weighs 0)", got.Command)
	}
}

func TestCombineFarApartKeepsPrior(t *testing.T) {
	prior := recall.DefaultWeights()
	t0 := time.Unix(100, 0)
	snaps := []Snapshot{snap("a", 100, recall.StepRule, 0.1), snap("b", 900, recall.StepRule, 1.9)}
	got := Combine(prior, snaps, []time.Time{t0, t0})
	if got.Command != prior.Command {
		t.Fatalf("Command = %v, want prior %v", got.Command, prior.Command)
	}
}

func TestCombineCloseShipsWeightedMean(t *testing.T) {
	prior := recall.DefaultWeights()
	t0 := time.Unix(100, 0)
	a, b := snap("a", 100, recall.StepRule, 1), snap("b", 900, recall.StepRule, 1)
	a.Weights.Key, b.Weights.Key = 0.8, 1.0
	got := Combine(prior, []Snapshot{a, b}, []time.Time{t0, t0})
	want := (0.8*100 + 1.0*500) / 600
	if math.Abs(got.Key-want) > 1e-9 {
		t.Fatalf("Key = %v, want weighted mean %v", got.Key, want)
	}
}

func TestCombineLatestPerInstall(t *testing.T) {
	prior := recall.DefaultWeights()
	old, newer := snap("a", 100, recall.StepRule, 0.5), snap("a", 100, recall.StepRule, 0.7)
	early, late := time.Unix(100, 0), time.Unix(200, 0)
	got := Combine(prior, []Snapshot{newer, old}, []time.Time{early, late})
	if got.Command != 0.5 {
		t.Fatalf("Command = %v, want the later file's 0.5", got.Command)
	}
}

func TestCombineSkipsOtherRules(t *testing.T) {
	prior := recall.DefaultWeights()
	t0 := time.Unix(100, 0)
	got := Combine(prior, []Snapshot{snap("a", 100, "S0", 0.1)}, []time.Time{t0})
	if got.Command != prior.Command {
		t.Fatalf("Command = %v, want prior: S0 snapshot must be ignored", got.Command)
	}
}

func TestCombinePinsText(t *testing.T) {
	prior := recall.DefaultWeights()
	prior.Text = 0.5
	got := Combine(prior, []Snapshot{snap("a", 100, recall.StepRule, 1)}, []time.Time{time.Unix(1, 0)})
	if got.Text != 1 {
		t.Fatalf("Text = %v, want 1", got.Text)
	}
}

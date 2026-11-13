package recall

import (
	"math"
	"math/rand"
	"testing"
)

func randFeatures(rng *rand.Rand) Features {
	return Features{
		Text: rng.Float64(), Key: rng.Float64(), Body: rng.Float64(), Type: rng.Float64(),
		Scope: rng.Float64(), Agent: rng.Float64(), Command: rng.Float64(), Trajectory: rng.Float64(),
	}
}

func sameWeights(a, b Weights, tol float64) bool {
	close := func(x, y float64) bool { return math.Abs(x-y) <= tol }
	return close(a.Text, b.Text) && close(a.Key, b.Key) && close(a.Body, b.Body) &&
		close(a.Type, b.Type) && close(a.Scope, b.Scope) && close(a.Agent, b.Agent) &&
		close(a.Command, b.Command) && close(a.Trajectory, b.Trajectory) &&
		a.Seen == b.Seen && a.Hits == b.Hits && a.Misses == b.Misses && a.StepRule == b.StepRule
}

func weightsIgnoringCounts(w Weights) Weights {
	w.Seen, w.Hits, w.Misses = 0, 0, 0
	return w
}

func siblingGroup(scale float64) Group {
	g := Group{{F: Features{Text: .5, Key: 1, Command: 1}, Label: 1, Reason: "full_command", Scale: scale}}
	for i := 0; i < 4; i++ {
		g = append(g, Row{F: Features{Text: .4, Key: .6, Type: 1}, Label: 0, Reason: "sibling", Scale: scale})
	}
	return g
}

func TestRefitPinsText(t *testing.T) {
	prior := DefaultWeights()
	prior.Text = 0.3
	out := Refit(prior, []Group{{{F: Features{Text: .5, Key: 1}, Label: 0, Reason: "failed", Scale: 1}}})
	if out.Text != 1 {
		t.Fatalf("Text = %v, want 1", out.Text)
	}
}

func TestRefitSingleFailedLineKeepsWeights(t *testing.T) {
	g := Group{{F: Features{Text: .5, Key: 1, Command: 1, Trajectory: 1}, Label: 0, Reason: "failed", Scale: 1}}
	out := Refit(DefaultWeights(), []Group{g})
	if !(out.Key > 0.5 && out.Command > 0.5 && out.Trajectory > 0.5) {
		t.Fatalf("Key, Command, Trajectory = %v, %v, %v; want each above 0.5", out.Key, out.Command, out.Trajectory)
	}
}

func TestRefitSiblingsBehindHitMoveNothing(t *testing.T) {
	out := Refit(DefaultWeights(), []Group{siblingGroup(.25)})
	if !sameWeights(weightsIgnoringCounts(out), weightsIgnoringCounts(DefaultWeights()), 0) {
		t.Fatalf("weights moved: %+v", out)
	}
}

func TestRefitSiblingAheadPullsDown(t *testing.T) {
	g := Group{
		{F: Features{Text: .5, Key: .5}, Label: 1, Reason: "full_command", Scale: 1},
		{F: Features{Text: .5, Type: 1}, Label: 0, Reason: "sibling", Scale: 1},
	}
	out := Refit(DefaultWeights(), []Group{g})
	if out.Type >= DefaultWeights().Type {
		t.Fatalf("Type = %v, want below default %v", out.Type, DefaultWeights().Type)
	}
}

func TestRefitCappedWrongLineMoves(t *testing.T) {
	g := Group{{F: Features{Text: .5, Key: 1, Command: 1}, Label: 0, Reason: "corrected", Scale: 1}}
	out := Refit(DefaultWeights(), []Group{g})
	d := DefaultWeights()
	if !(out.Key < d.Key || out.Command < d.Command) {
		t.Fatalf("Key, Command = %v, %v; want one below default", out.Key, out.Command)
	}
}

func TestRefitZeroWeightCanRise(t *testing.T) {
	prior := DefaultWeights()
	prior.Body = 0
	g := Group{{F: Features{Text: .5, Body: 1}, Label: 1, Reason: "full_command", Scale: 1}}
	out := Refit(prior, []Group{g})
	if out.Body <= 0 {
		t.Fatalf("Body = %v, want above 0", out.Body)
	}
}

func TestRefitOrderFree(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	reasons := []string{"full_command", "forgotten", "corrected", "failed", "sibling"}
	groups := make([]Group, 20)
	for i := range groups {
		for j := 0; j < 1+rng.Intn(4); j++ {
			reason := reasons[rng.Intn(len(reasons))]
			label := 0
			if reason == "full_command" {
				label = 1
			}
			groups[i] = append(groups[i], Row{F: randFeatures(rng), Label: label, Reason: reason, Scale: rng.Float64()})
		}
	}
	want := Refit(DefaultWeights(), groups)

	shuffled := make([]Group, len(groups))
	for i := range groups {
		shuffled[i] = append(Group(nil), groups[i]...)
		rng.Shuffle(len(shuffled[i]), func(a, b int) { shuffled[i][a], shuffled[i][b] = shuffled[i][b], shuffled[i][a] })
	}
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	if got := Refit(DefaultWeights(), shuffled); !sameWeights(got, want, 1e-9) {
		t.Fatalf("shuffled refit = %+v, want %+v", got, want)
	}
}

func TestBlendRawMatchesBlend(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	w := DefaultWeights()
	for i := 0; i < 50; i++ {
		f := randFeatures(rng)
		r, _, _ := blendRaw(f, w)
		if got, want := clamp(r, 0, 1), blend(f, w); math.Abs(got-want) > 1e-12 {
			t.Fatalf("clamp(blendRaw) = %v, blend = %v for %+v", got, want, f)
		}
	}
}

func TestRefitCounts(t *testing.T) {
	groups := []Group{
		{{F: Features{Text: .5, Key: 1}, Label: 1, Reason: "full_command", Scale: 1},
			{F: Features{Text: .5}, Label: 0, Reason: "sibling", Scale: 1}},
		{{F: Features{Text: .5, Key: 1}, Label: 0, Reason: "failed", Scale: 1},
			{F: Features{Text: .5, Key: 1}, Label: 0, Reason: "corrected", Scale: 1}},
	}
	out := Refit(DefaultWeights(), groups)
	if out.Seen != 2 || out.Hits != 1 || out.Misses != 2 {
		t.Fatalf("Seen/Hits/Misses = %d/%d/%d, want 2/1/2", out.Seen, out.Hits, out.Misses)
	}
}

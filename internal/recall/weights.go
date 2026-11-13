package recall

import (
	"context"
	"encoding/json"

	"github.com/a6n-ai/inscribe/internal/store"
)

const (
	LabelRule = "L1"
	StepRule  = "S1"
)

func weightsKey() string { return "score_model." + StepRule }

// Weights scale the column features. Text is the floor and is pinned at 1.
// The refit moves the others when an injection later worked or failed.
// Defaults keep a text-only match on the same number the text grade already produced.
type Weights struct {
	Text       float64 `json:"text"`
	Key        float64 `json:"key"`
	Body       float64 `json:"body"`
	Type       float64 `json:"type"`
	Scope      float64 `json:"scope"`
	Agent      float64 `json:"agent"`
	Command    float64 `json:"command"`
	Trajectory float64 `json:"trajectory"`
	Seen       int     `json:"seen"`
	Hits       int     `json:"hits"`
	Misses     int     `json:"misses"`
	StepRule   string  `json:"step_rule"`
}

// DefaultWeights is the model before any idle pass has run.
func DefaultWeights() Weights {
	return Weights{Text: 1, Key: 1, Body: 0.85, Type: 0.4, Scope: 1, Agent: 0.5, Command: 1, Trajectory: 1, StepRule: StepRule}
}

// LoadWeights reads the score model. A database that has never trained
// returns DefaultWeights with an empty StepRule, so Train replays it.
func LoadWeights(st *store.Store) (Weights, error) {
	raw, ok, err := st.GetMeta(weightsKey())
	return decodeWeights(raw, ok, err)
}

// LoadWeightsTx is LoadWeights inside an open transaction.
func LoadWeightsTx(tx *store.Tx) (Weights, error) {
	raw, ok, err := tx.GetMeta(weightsKey())
	return decodeWeights(raw, ok, err)
}

func decodeWeights(raw string, ok bool, err error) (Weights, error) {
	if err != nil || !ok || raw == "" {
		w := DefaultWeights()
		w.StepRule = ""
		return w, err
	}
	w := DefaultWeights()
	w.StepRule = ""
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return DefaultWeights(), err
	}
	w.Text = 1
	return w, nil
}

// SaveWeights stores the score model.
func SaveWeights(ctx context.Context, st *store.Store, w Weights) error {
	return st.Write(ctx, func(tx *store.Tx) error { return SaveWeightsTx(tx, w) })
}

// SaveWeightsTx is SaveWeights inside an open transaction.
func SaveWeightsTx(tx *store.Tx, w Weights) error {
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return tx.SetMeta(weightsKey(), string(raw))
}

func (f Features) marshal() (string, error) {
	b, err := json.Marshal([8]float64{f.Text, f.Key, f.Body, f.Type, f.Scope, f.Agent, f.Command, f.Trajectory})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseFeatures(s string) (Features, error) {
	var a []float64
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return Features{}, err
	}
	get := func(i int) float64 {
		if i < len(a) {
			return a[i]
		}
		return 0
	}
	return Features{Text: get(0), Key: get(1), Body: get(2), Type: get(3), Scope: get(4), Agent: get(5), Command: get(6), Trajectory: get(7)}, nil
}

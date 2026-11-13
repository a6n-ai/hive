package model

import (
	"math"
	"testing"
)

func TestEffectiveConfidence(t *testing.T) {
	if got := (Memory{Confidence: 0.9, Penalty: 0.2}).EffectiveConfidence(); math.Abs(got-0.7) > 1e-9 {
		t.Fatalf("got %v", got)
	}
	if got := (Memory{Confidence: 0.1, Penalty: 0.4}).EffectiveConfidence(); got != 0 {
		t.Fatalf("floor: got %v", got)
	}
}

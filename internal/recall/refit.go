package recall

import "math"

const (
	refitStep     = 0.5
	siblingMargin = 0.1
	priorPull     = 0.1
	refitPasses   = 8
	refitStopMove = 0.001
	weightMax     = 2.0
)

// Row is one labeled injection. Scale weights its contribution to the refit.
type Row struct {
	F      Features
	Label  int
	Reason string
	Scale  float64
}

// Group is the injections shown in one session for one prompt.
type Group []Row

// weightVec lists the learned fields in blendRaw's d order.
func weightVec(w Weights) [7]float64 {
	return [7]float64{w.Key, w.Body, w.Type, w.Scope, w.Agent, w.Command, w.Trajectory}
}

// Refit is a full-batch projected gradient over the learned weights. The
// result does not depend on group order. Text stays pinned at 1.
func Refit(prior Weights, groups []Group) Weights {
	out := prior
	out.Text = 1
	out.StepRule = StepRule
	out.Seen = len(groups)
	out.Hits, out.Misses = 0, 0
	for _, g := range groups {
		for _, r := range g {
			if r.Label == 1 {
				out.Hits++
			} else if r.Label == 0 && r.Reason != "sibling" {
				out.Misses++
			}
		}
	}
	if len(groups) == 0 {
		return out
	}

	def := weightVec(DefaultWeights())
	n := float64(len(groups))
	for pass := 0; pass < refitPasses; pass++ {
		wv := weightVec(out)
		grad := refitGradient(out, groups)
		ptrs := [7]*float64{&out.Key, &out.Body, &out.Type, &out.Scope, &out.Agent, &out.Command, &out.Trajectory}
		moved := false
		for i := range grad {
			g := grad[i]/n + priorPull*(def[i]-wv[i])
			next := clamp(wv[i]+refitStep*g, 0, weightMax)
			if math.Abs(next-wv[i]) >= refitStopMove {
				moved = true
			}
			*ptrs[i] = next
		}
		if !moved {
			break
		}
	}
	return out
}

// refitGradient sums the per-row and sibling terms at the weights w.
func refitGradient(w Weights, groups []Group) [7]float64 {
	var grad [7]float64
	for _, g := range groups {
		for _, row := range g {
			switch row.Reason {
			case "full_command", "forgotten", "corrected", "failed":
				r, floor, d := blendRaw(row.F, w)
				capped := math.Min(1, r)
				var err float64
				if row.Label == 1 {
					err = 1 - capped
				} else {
					err = -(capped - floor)
				}
				for i := range grad {
					grad[i] += row.Scale * err * d[i]
				}
			}
		}
		for _, h := range g {
			if h.Reason != "full_command" {
				continue
			}
			rh, _, dh := blendRaw(h.F, w)
			for _, s := range g {
				if s.Reason != "sibling" {
					continue
				}
				rs, _, ds := blendRaw(s.F, w)
				if rs <= rh-siblingMargin {
					continue
				}
				for i := range grad {
					grad[i] += s.Scale * h.Scale * (dh[i] - ds[i])
				}
			}
		}
	}
	return grad
}

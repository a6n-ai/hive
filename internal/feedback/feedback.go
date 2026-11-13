// Package feedback turns agent answers about injected memories into training
// labels, penalties and the text that asks the agent for those answers.
package feedback

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/a6n-ai/inscribe/model"
)

// Answer is one agent's answer about one injected memory.
type Answer struct {
	ID      string `json:"id"`
	Used    string `json:"used"`
	Helpful string `json:"helpful"`
	How     string `json:"how,omitempty"`
}

const (
	AgentScale  = 0.5
	ReviewScale = 0.3
)

// Label maps an answer to a training label, its scale and its reason. A
// partly-used answer, or a used=yes with no how, halves the scale; so does a
// used=yes answer the agent found unhelpful.
func Label(a Answer, scale float64, reason string) (label int, outScale float64, outReason string, wrong bool, err error) {
	switch a.Used {
	case "yes", "partly", "no":
	default:
		return 0, 0, "", false, fmt.Errorf("feedback: used %q is not yes, partly or no", a.Used)
	}
	switch a.Helpful {
	case "yes", "no", "wrong":
	default:
		return 0, 0, "", false, fmt.Errorf("feedback: helpful %q is not yes, no or wrong", a.Helpful)
	}

	used := a.Used
	if used == "yes" && strings.TrimSpace(a.How) == "" {
		used = "partly"
	}
	if a.Helpful == "wrong" {
		return 0, scale, reason + "_wrong", true, nil
	}
	if used != "no" && a.Helpful == "yes" {
		label = 1
	}
	if used == "partly" || (used == "yes" && a.Helpful == "no") {
		scale /= 2
	}
	return label, scale, reason, false, nil
}

// Penalty is subtracted from a memory's confidence when the agent calls it
// wrong. Global memories cost less because they apply in every project.
func Penalty(m model.Memory) float64 {
	if m.ScopeKind == model.Global {
		return 0.05
	}
	return 0.2
}

func NewAskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "a_" + hex.EncodeToString(b)
}

// Render is the text that asks the agent to answer about ms.
func Render(askID string, ms []model.Memory) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "inscribe feedback %s: you were given these memories. Call the inscribe feedback tool with ask %q and, for each id, used (yes|partly|no), helpful (yes|no|wrong), and for used ones a short \"how\".", askID, askID)
	for _, m := range ms {
		fmt.Fprintf(&sb, "\n- %s [%s] %s", m.ID, m.Type, CutRunes(m.Text, 90))
	}
	return sb.String()
}

// CutRunes collapses s to one line of at most n runes, marking a cut with an ellipsis.
func CutRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

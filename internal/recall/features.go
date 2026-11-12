package recall

import (
	"math"
	"sort"
	"strings"

	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

// Features is how a prompt lands on one memory. Each value is 0..1.
// Text is the prompt against the memory text. The others are the stored
// columns and the command/agent chunks of the prompt.
type Features struct {
	Text, Key, Body, Type, Scope, Agent, Command, Trajectory float64
}

func fieldFeatures(groups [][]string, facets Facets, m model.Memory) Features {
	body := bodyText(m.Body)
	keyTerms := text.Terms(m.Key)
	bodyTerms := text.Terms(body)
	f := Features{
		Text:  grade(groups, text.Terms(m.Text)),
		Key:   grade(groups, keyTerms),
		Body:  grade(groups, bodyTerms),
		Scope: grade(groups, text.Terms(string(m.ScopeKind)+" "+m.Scope)),
		Type:  typeFeature(facets.Types, m.Type),
		Agent: agentFeature(facets.Agents, m.Text+" "+m.Key+" "+m.Scope+" "+body),
	}
	cmdTerms := append(append([]string{}, keyTerms...), bodyTerms...)
	for _, g := range facets.Commands {
		if s := grade(g, cmdTerms); s > f.Command {
			f.Command = s
		}
	}
	return f
}

// blend keeps the text grade as the floor, then adds a column only when that
// column matches more of the prompt than the text does. A type or agent hit
// on its own stays small, so naming "fix" does not inject every fix.
func blend(f Features, w Weights) float64 {
	base := math.Min(1, w.Text*f.Text)
	extra := 0.0
	add := func(v, weight float64) {
		if weight > 0 && v > base {
			extra += weight * (v - base)
		}
	}
	add(f.Key, w.Key)
	add(f.Body, w.Body)
	add(f.Scope, w.Scope)
	add(f.Command, w.Command)
	add(f.Type, w.Type)
	add(f.Agent, w.Agent)
	add(f.Trajectory, w.Trajectory)
	g := base + extra
	if g > 1 {
		return 1
	}
	if g < 0 {
		return 0
	}
	return g
}

// applyTrajectory sets the grade of the stored trajectory JSON for this memory.
// The JSON is the same object written at capture.
func applyTrajectory(f Features, groups [][]string, traj string) Features {
	if traj == "" {
		return f
	}
	if g := grade(groups, text.Terms(traj)); g > f.Trajectory {
		f.Trajectory = g
	}
	return f
}

func typeFeature(types []model.MemoryType, got model.MemoryType) float64 {
	for _, t := range types {
		if t == got {
			return 1
		}
	}
	return 0
}

func agentFeature(agents []string, blob string) float64 {
	blob = strings.ToLower(blob)
	for _, a := range agents {
		if a != "" && strings.Contains(blob, a) {
			return 1
		}
	}
	return 0
}

func bodyText(body map[string]any) string {
	if len(body) == 0 {
		return ""
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		writeBody(&b, body[k])
	}
	return b.String()
}

func writeBody(b *strings.Builder, v any) {
	switch x := v.(type) {
	case string:
		b.WriteString(x)
		b.WriteByte(' ')
	case []any:
		for _, item := range x {
			writeBody(b, item)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeBody(b, x[k])
		}
	}
}

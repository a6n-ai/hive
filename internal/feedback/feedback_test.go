package feedback

import (
	"regexp"
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

func TestLabelTable(t *testing.T) {
	tests := []struct {
		name       string
		a          Answer
		wantLabel  int
		wantScale  float64
		wantReason string
		wantWrong  bool
	}{
		{"yes how set, helpful yes", Answer{Used: "yes", Helpful: "yes", How: "ran it"}, 1, 0.5, "agent", false},
		{"partly, helpful yes", Answer{Used: "partly", Helpful: "yes"}, 1, 0.25, "agent", false},
		{"yes how set, helpful no", Answer{Used: "yes", Helpful: "no", How: "ran it"}, 0, 0.25, "agent", false},
		{"no, helpful no", Answer{Used: "no", Helpful: "no"}, 0, 0.5, "agent", false},
		{"no, helpful yes", Answer{Used: "no", Helpful: "yes"}, 0, 0.5, "agent", false},
		{"any used, helpful wrong", Answer{Used: "yes", Helpful: "wrong", How: "ran it"}, 0, 0.5, "agent_wrong", true},
		{"wrong regardless of used", Answer{Used: "no", Helpful: "wrong"}, 0, 0.5, "agent_wrong", true},
		{"yes with blank how treated as partly", Answer{Used: "yes", Helpful: "yes", How: "   "}, 1, 0.25, "agent", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, scale, reason, wrong, err := Label(tt.a, AgentScale, "agent")
			if err != nil {
				t.Fatalf("Label: %v", err)
			}
			if label != tt.wantLabel || scale != tt.wantScale || reason != tt.wantReason || wrong != tt.wantWrong {
				t.Fatalf("got label=%d scale=%v reason=%q wrong=%v; want label=%d scale=%v reason=%q wrong=%v",
					label, scale, reason, wrong, tt.wantLabel, tt.wantScale, tt.wantReason, tt.wantWrong)
			}
		})
	}
}

func TestLabelRejectsBadEnum(t *testing.T) {
	if _, _, _, _, err := Label(Answer{Used: "maybe", Helpful: "yes"}, AgentScale, "agent"); err == nil || !strings.Contains(err.Error(), "maybe") {
		t.Fatalf("bad used: want error naming %q, got %v", "maybe", err)
	}
	if _, _, _, _, err := Label(Answer{Used: "yes", Helpful: "sure"}, AgentScale, "agent"); err == nil || !strings.Contains(err.Error(), "sure") {
		t.Fatalf("bad helpful: want error naming %q, got %v", "sure", err)
	}
}

func TestPenaltyByScope(t *testing.T) {
	if got := Penalty(model.Memory{ScopeKind: model.ProjectScope}); got != 0.2 {
		t.Fatalf("project penalty = %v, want 0.2", got)
	}
	if got := Penalty(model.Memory{ScopeKind: model.Global}); got != 0.05 {
		t.Fatalf("global penalty = %v, want 0.05", got)
	}
}

func TestNewAskID(t *testing.T) {
	re := regexp.MustCompile(`^a_[0-9a-f]{32}$`)
	a, b := NewAskID(), NewAskID()
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("ask ids %q, %q do not match %s", a, b, re)
	}
	if a == b {
		t.Fatalf("two ask ids are equal: %q", a)
	}
}

func TestRender(t *testing.T) {
	long := strings.Repeat("x", 300)
	ms := []model.Memory{
		{ID: "m_1", Type: model.Command, Text: long},
		{ID: "m_2", Type: model.Fix, Text: "short text"},
	}
	out := Render("a_abc", ms)
	for _, want := range []string{"a_abc", "feedback", "m_1", "m_2", "[command]", "[fix]", "short text"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, strings.Repeat("x", 90)+"…") || strings.Contains(out, strings.Repeat("x", 91)) {
		t.Fatalf("long text not cut to 90 runes plus …:\n%s", out)
	}
}

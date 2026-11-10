package extract_test

import (
	"testing"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/extract"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

func (h *harness) correction(session, prompt string) {
	h.emit(session, "/p", model.TurnEnd, "", nil, "")
	h.emit(session, "/p", model.Prompt, "", nil, prompt)
}

func (h *harness) prefs() []model.Memory {
	h.t.Helper()
	ms, err := h.p.Store.ListMemories(store.ListFilter{Types: []model.MemoryType{model.Preference}})
	if err != nil {
		h.t.Fatal(err)
	}
	return ms
}

func TestPreferenceCandidateThenPromoted(t *testing.T) {
	h := newHarness(t, config.Defaults(), extract.NewPreference())
	h.correction("s1", "no, use rg not grep")
	if ms := h.prefs(); len(ms) != 1 || ms[0].Status != model.Candidate || ms[0].Key != "use rg not grep" {
		t.Fatalf("after first: %+v", ms)
	}
	h.correction("s1", "use rg not grep")
	if ms := h.prefs(); ms[0].Status != model.Candidate {
		t.Fatalf("same session promoted: %+v", ms[0])
	}
	h.correction("s2", "no, use rg not grep")
	if ms := h.prefs(); ms[0].Status != model.Active || ms[0].ScopeKind != model.Global {
		t.Fatalf("after second session: %+v", ms[0])
	}
}

func (h *harness) loadSkill(session, skill string) {
	h.emit(session, "/p", model.PostTool, "skill", map[string]any{"skill": skill}, "")
}

func skillCfg(enabled string, enrich ...string) config.Config {
	c := config.Defaults()
	c.Skills.Enabled = config.Tristate(enabled)
	c.Skills.Enrich = enrich
	return c
}

func TestSkillMemoryOffByDefaultWithoutScout(t *testing.T) {
	h := newHarness(t, skillCfg("auto"), extract.NewSkill(false))
	h.loadSkill("s1", "superpowers:brainstorming")
	if ms := h.list(model.SkillUse); len(ms) != 0 {
		t.Fatalf("skill memory without Scout: %+v", ms)
	}
}

func TestSkillMemoryEnabledTrueWithoutScout(t *testing.T) {
	h := newHarness(t, skillCfg("true"), extract.NewSkill(false))
	h.loadSkill("s1", "superpowers:brainstorming")
	ms := h.list(model.SkillUse)
	if len(ms) != 1 || ms[0].Key != "superpowers:brainstorming|/p" {
		t.Fatalf("skill_use = %+v", ms)
	}
}

func TestSkillMemoryAutoWithScout(t *testing.T) {
	h := newHarness(t, skillCfg("auto"), extract.NewSkill(true))
	h.loadSkill("s1", "tdd")
	if ms := h.list(model.SkillUse); len(ms) != 1 {
		t.Fatalf("auto with Scout: %+v", ms)
	}
}

func TestSkillFlowConsecutive(t *testing.T) {
	h := newHarness(t, skillCfg("true"), extract.NewSkill(false))
	h.loadSkill("s1", "A")
	h.emit("s1", "/p", model.Prompt, "", nil, "next")
	h.loadSkill("s1", "B")
	ms := h.list(model.SkillFlow)
	if len(ms) != 1 || ms[0].Key != "A>B|/p" {
		t.Fatalf("skill_flow = %+v", ms)
	}
}

func TestEnrichmentNeverChangesKey(t *testing.T) {
	run := func(cfg config.Config) model.Memory {
		h := newHarness(t, cfg, extract.NewSkill(false))
		h.emit("s1", "/p", model.Prompt, "", nil, "fix the failing login test in auth middleware")
		h.loadSkill("s1", "systematic-debugging")
		ms := h.list(model.SkillUse)
		if len(ms) != 1 {
			t.Fatalf("skill_use = %+v", ms)
		}
		return ms[0]
	}
	plain := run(skillCfg("true"))
	rich := run(skillCfg("true", "taxonomy", "terms"))
	if plain.Key != rich.Key {
		t.Fatalf("keys differ: %s vs %s", plain.Key, rich.Key)
	}
	if _, ok := plain.Body["classes"]; ok {
		t.Fatal("classes set without taxonomy enrichment")
	}
	classes, _ := rich.Body["classes"].(map[string]any)
	if classes["debug"] == nil {
		t.Fatalf("classes = %v", rich.Body["classes"])
	}
	terms, _ := rich.Body["terms"].(map[string]any)
	if terms["login"] == nil || len(terms) > 20 {
		t.Fatalf("terms = %v", rich.Body["terms"])
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"fix the failing login test":          "debug",
		"add coverage for the parser spec":    "test",
		"make the settings page layout nicer": "design",
		"deploy to production":                "deploy",
		"what time is it":                     "other",
	}
	for in, want := range cases {
		if got := extract.Classify(in); got != want {
			t.Errorf("Classify(%q) = %s, want %s", in, got, want)
		}
	}
}

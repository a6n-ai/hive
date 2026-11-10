package extract

import (
	"context"
	"slices"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

type skillX struct{ callerIsScout bool }

// NewSkill records skill use and skill-to-skill flow. It is active when
// skills.enabled is "true", or "auto" with Scout as the caller.
func NewSkill(callerIsScout bool) Extractor { return skillX{callerIsScout} }

func (skillX) Kinds() []model.EventKind { return []model.EventKind{model.SkillLoaded} }

func (x skillX) enabled(cfg config.Config) bool {
	switch cfg.Skills.Enabled {
	case "true":
		return true
	case "auto":
		return x.callerIsScout
	}
	return false
}

func (x skillX) Extract(_ context.Context, e *model.Event, tx *store.Tx, cfg config.Config) error {
	skill := str(e.Payload, "skill")
	if skill == "" || !x.enabled(cfg) {
		return nil
	}
	prior, err := tx.RecentEvents(e.Session, e.ID, lookback)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if prompt := lastPrompt(prior); prompt != "" {
		if slices.Contains(cfg.Skills.Enrich, "taxonomy") {
			body["classes"] = map[string]any{Classify(prompt): 1}
		}
		if slices.Contains(cfg.Skills.Enrich, "terms") {
			terms := map[string]any{}
			for _, t := range text.Terms(prompt) {
				terms[t] = 1
			}
			body["terms"] = terms
		}
	}
	use := projectMemory(e, model.SkillUse, skill+"|"+e.Project, "uses "+skill, body, model.Active)
	if _, err := tx.UpsertMemory(use, mergeCounts, e.ID); err != nil {
		return err
	}
	if prev := lastSkill(prior); prev != "" && prev != skill {
		flow := projectMemory(e, model.SkillFlow, prev+">"+skill+"|"+e.Project, prev+" then "+skill, nil, model.Active)
		if _, err := tx.UpsertMemory(flow, nil, e.ID); err != nil {
			return err
		}
	}
	return nil
}

// mergeCounts adds the count maps under "classes" and "terms", keeping the
// top maxTerms terms.
func mergeCounts(prev, next map[string]any) map[string]any {
	out := store.ShallowMerge(prev, next)
	for _, k := range []string{"classes", "terms"} {
		a, _ := prev[k].(map[string]any)
		b, _ := next[k].(map[string]any)
		if a == nil && b == nil {
			continue
		}
		sum := make(map[string]any, len(a)+len(b))
		for key, v := range a {
			sum[key] = countOf(v)
		}
		for key, v := range b {
			sum[key] = countOf(sum[key]) + countOf(v)
		}
		if k == "terms" {
			sum = topCounts(sum, maxTerms)
		}
		out[k] = sum
	}
	return out
}

// lastPrompt returns the newest prompt text in prior (newest first).
func lastPrompt(prior []model.Event) string {
	for i := range prior {
		if prior[i].Kind == model.Prompt {
			return prior[i].PromptText
		}
	}
	return ""
}

// lastSkill returns the newest skill loaded in prior (newest first).
func lastSkill(prior []model.Event) string {
	for i := range prior {
		if prior[i].Kind == model.SkillLoaded {
			return str(prior[i].Payload, "skill")
		}
	}
	return ""
}

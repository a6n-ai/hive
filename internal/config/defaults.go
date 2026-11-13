package config

// Defaults returns the shipped configuration.
func Defaults() Config {
	return Config{
		Recall:      RecallCfg{Inject: true, SessionBudget: 400, PromptMaxLines: 3, PromptThreshold: 0.35, HalfLifeDays: 30},
		Consolidate: ConsolidateCfg{Auto: true, MinEvents: 20, InputBudget: 8000, Model: map[string]string{"claude-code": "haiku"}},
		Retention:   RetentionCfg{Mode: "rolling", PromptTextDays: 30, PromptClock: "original", EventDays: 180, ReplaySampleMax: 20000, ReplayDays: 0},
		Skills:      SkillsCfg{Enabled: "auto", Enrich: []string{}},
		Scrub:       ScrubCfg{Patterns: []string{}},
		Exclude:     ExcludeCfg{Paths: []string{}},
	}
}

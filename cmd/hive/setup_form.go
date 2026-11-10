package main

import (
	"errors"
	"io"
	"slices"

	"github.com/charmbracelet/huh"

	"github.com/a6n-ai/hive/internal/setup"
)

// runSetupForm asks each step in the approved order, then shows the review.
// It reports false when the user cancels.
func runSetupForm(stdout io.Writer, d setup.Detected, c *setup.Choices) (bool, error) {
	groups := []*huh.Group{agentsGroup(d, c)}
	if d.ClaudeMem {
		groups = append(groups, claudeMemGroup(c))
	}
	if d.HasHistory {
		groups = append(groups, historyGroup(c))
	}
	groups = append(groups, retentionGroup(c), consolidateGroup(c), mcpGroup(c))
	if err := huh.NewForm(groups...).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	printPlan(stdout, d, *c)
	var confirm bool
	err := huh.NewConfirm().Title("Apply these changes?").Affirmative("Apply").Negative("Cancel").Value(&confirm).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	return confirm, err
}

func agentsGroup(d setup.Detected, c *setup.Choices) *huh.Group {
	var opts []huh.Option[string]
	for _, a := range d.Agents {
		label := a.Name
		if !a.Verified {
			label += " (not yet verified against a live CLI)"
		}
		opts = append(opts, huh.NewOption(label, a.Name).Selected(slices.Contains(c.Agents, a.Name)))
	}
	return huh.NewGroup(huh.NewMultiSelect[string]().Title("Agents to hook").Options(opts...).Value(&c.Agents))
}

func claudeMemGroup(c *setup.Choices) *huh.Group {
	return huh.NewGroup(huh.NewConfirm().Title("Disable claude-mem? Hive replaces it.").
		Value(&c.DisableClaudeMem))
}

func historyGroup(c *setup.Choices) *huh.Group {
	return huh.NewGroup(huh.NewSelect[string]().Title("Import Claude Code history").Options(
		huh.NewOption("none", ""),
		huh.NewOption("last 30 days (recommended)", "30d"),
		huh.NewOption("last 90 days", "90d"),
		huh.NewOption("all history", "all"),
	).Value(&c.ImportSince))
}

func retentionGroup(c *setup.Choices) *huh.Group {
	return huh.NewGroup(huh.NewSelect[string]().Title("Retention").Options(
		huh.NewOption("rolling (recommended): prune events older than event_days", "rolling"),
		huh.NewOption("storage_time: prune by storage size", "storage_time"),
		huh.NewOption("keep_all: never prune", "keep_all"),
	).Value(&c.Retention))
}

func consolidateGroup(c *setup.Choices) *huh.Group {
	return huh.NewGroup(huh.NewConfirm().Title("Consolidate memories with a model after sessions?").
		Description("Off by default. Each run makes a model call, which costs money.").
		Value(&c.Consolidate))
}

func mcpGroup(c *setup.Choices) *huh.Group {
	return huh.NewGroup(huh.NewConfirm().Title("Add Hive as an MCP server to Claude Code?").Value(&c.MCP))
}

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe"
)

func TestRecentLinesSplitsProjectsAndTrims(t *testing.T) {
	now := time.UnixMilli(10 * 3600_000)
	rs := []inscribe.RecentSession{
		{Project: "/w/other", LastPrompt: "fix  the\nbuild", LastEvent: now.Add(-30 * time.Minute).UnixMilli()},
		{Project: "/w/here", LastPrompt: strings.Repeat("a", 150), LastEvent: now.Add(-3 * time.Hour).UnixMilli()},
	}
	lines := recentLines(rs, "/w/here", now)
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	if want := "this project, 3h ago: " + strings.Repeat("a", 100) + "…"; lines[0].Text != want {
		t.Errorf("here = %q", lines[0].Text)
	}
	if lines[1].Text != "other, 30m ago: fix the build" || lines[1].Type != "recent" {
		t.Errorf("other = %+v", lines[1])
	}
}

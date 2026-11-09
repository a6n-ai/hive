package importer_test

import (
	"context"
	"testing"

	"github.com/a6n-ai/hive/internal/importer"
	"github.com/a6n-ai/hive/model"
)

// TestImportSkipsConsolidationTranscripts: a headless review transcript must not
// come back in as events.
func TestImportSkipsConsolidationTranscripts(t *testing.T) {
	var sessions []string
	sink := func(_ context.Context, e model.Event) error {
		sessions = append(sessions, e.Session)
		return nil
	}
	sum, err := importer.ClaudeCode(context.Background(), importer.Options{Root: "testdata/headless/projects"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 1 || len(sessions) != 0 {
		t.Errorf("files = %d, events from sessions %v; want the review transcript skipped", sum.Files, sessions)
	}
}

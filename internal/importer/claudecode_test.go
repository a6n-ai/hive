package importer_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a6n-ai/inscribe"
	"github.com/a6n-ai/inscribe/internal/importer"
	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

const fixtureRoot = "testdata/projects"

// openHome opens an Inscribe on a temp home with skills enabled, so skill_use memories form.
func openHome(t *testing.T) (*inscribe.Inscribe, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[skills]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := inscribe.Open(inscribe.Options{Home: home, Caller: "inscribe"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, home
}

func importFixture(t *testing.T, h *inscribe.Inscribe, o importer.Options) inscribe.ImportReport {
	t.Helper()
	if o.Root == "" {
		o.Root = fixtureRoot
	}
	rep, err := h.ImportClaudeCode(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func memoryTypes(t *testing.T, h *inscribe.Inscribe) map[model.MemoryType]int {
	t.Helper()
	ms, err := h.List(context.Background(), store.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[model.MemoryType]int{}
	for _, m := range ms {
		out[m.Type]++
	}
	return out
}

func TestImportBuildsEventsAndMemories(t *testing.T) {
	h, _ := openHome(t)
	rep := importFixture(t, h, importer.Options{})
	if rep.Files != 1 || rep.Skipped != 3 {
		t.Fatalf("files=%d skipped=%d, want 1 and 3 (garbage, sidechain, unknown type)", rep.Files, rep.Skipped)
	}
	types := memoryTypes(t, h)
	for _, want := range []model.MemoryType{model.Command, model.Fix, model.SkillUse} {
		if types[want] == 0 {
			t.Errorf("no %s memory from the fixture; got %v", want, types)
		}
	}
}

func TestImportIsIdempotent(t *testing.T) {
	h, _ := openHome(t)
	first := importFixture(t, h, importer.Options{})
	if first.NewEvents == 0 {
		t.Fatal("first import stored no events")
	}
	second := importFixture(t, h, importer.Options{})
	if second.NewEvents != 0 || second.NewMemories != 0 {
		t.Errorf("second import new_events=%d new_memories=%d, want 0 and 0", second.NewEvents, second.NewMemories)
	}
}

func TestImportScrubs(t *testing.T) {
	h, home := openHome(t)
	rep := importFixture(t, h, importer.Options{})
	if rep.Redactions < 1 {
		t.Errorf("redactions = %d, want at least 1", rep.Redactions)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inscribe.db", "inscribe.db-wal"} {
		b, err := os.ReadFile(filepath.Join(home, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "hunter9two") {
			t.Errorf("%s contains the secret", name)
		}
	}
}

func TestImportSinceAndProject(t *testing.T) {
	h, _ := openHome(t)
	all := importFixture(t, h, importer.Options{})

	late := importer.Options{Since: time.Date(2026, 11, 1, 10, 0, 9, 0, time.UTC)}
	if got := importFixture(t, h, late); got.Events == 0 || got.Events >= all.Events {
		t.Errorf("since: events=%d, want between 1 and %d", got.Events, all.Events-1)
	}

	other := importFixture(t, h, importer.Options{Project: "/does/not/exist"})
	if other.Events != 0 || other.NewEvents != 0 {
		t.Errorf("project filter: events=%d new_events=%d, want 0 and 0", other.Events, other.NewEvents)
	}
}

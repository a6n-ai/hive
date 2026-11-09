package extract

import (
	"context"
	"strings"

	"github.com/a6n-ai/hive/internal/config"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

type fixX struct{}

// NewFix links an error, the edits after it, and the passing run that ended
// it, keyed by the error's signature.
func NewFix() Extractor { return fixX{} }

func (fixX) Kinds() []model.EventKind { return []model.EventKind{model.CommandResult} }

func (fixX) Extract(_ context.Context, e *model.Event, tx *store.Tx, _ config.Config) error {
	if code, ok := intVal(e.Payload, "exit_code"); !ok || code != 0 {
		return nil
	}
	class := ClassifyCommand(NormalizeCommand(str(e.Payload, "command")))
	if class != "test" && class != "build" {
		return nil
	}
	prior, err := tx.RecentEvents(e.Session, e.ID, lookback)
	if err != nil {
		return err
	}
	errEv, edits := findFailureAndEdits(prior, class)
	if errEv == nil || len(edits) == 0 {
		return nil
	}
	errLine := firstLine(str(errEv.Payload, "error"))
	var files []string
	seen := map[string]bool{}
	for _, ed := range edits {
		p, _ := isEdit(ed)
		if r := relPath(e.Project, p); !seen[r] {
			seen[r] = true
			files = append(files, r)
		}
	}
	green := map[string]string{"test": "tests green", "build": "build green"}[class]
	text := truncate(errLine, 120) + " → edited " + strings.Join(files, ", ") + " → " + green
	m, err := tx.UpsertMemory(projectMemory(e, model.Fix, Signature(errLine), text,
		map[string]any{"files": files, "error": errLine}, model.Active), nil, e.ID)
	if err != nil {
		return err
	}
	for _, ev := range append([]*model.Event{errEv}, edits...) {
		if err := tx.AddEvidence(m.ID, ev.ID); err != nil {
			return err
		}
	}
	return nil
}

// findFailureAndEdits walks back from newest: it collects edits until it
// reaches the most recent failure. A passing run of the same class first means
// there is nothing to link.
func findFailureAndEdits(prior []model.Event, class string) (*model.Event, []*model.Event) {
	var edits []*model.Event
	for i := range prior {
		ev := &prior[i]
		if _, ok := isEdit(ev); ok {
			edits = append(edits, ev)
			continue
		}
		if ev.Kind != model.CommandResult {
			continue
		}
		if ClassifyCommand(NormalizeCommand(str(ev.Payload, "command"))) != class {
			continue
		}
		if code, _ := intVal(ev.Payload, "exit_code"); code == 0 {
			return nil, nil
		}
		if str(ev.Payload, "error") != "" {
			return ev, edits
		}
	}
	return nil, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

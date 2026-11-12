package recall

import (
	"strings"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/internal/trajectory"
	"github.com/a6n-ai/inscribe/model"
)

// mergeTrajectory adds memories cited by a trajectory record whose JSON
// matches the prompt. The record is the same object written at capture.
// traj maps a memory id to the matched JSON text.
func mergeTrajectory(src Source, f store.ListFilter, groups [][]string, ms []model.Memory) ([]model.Memory, map[string]string, error) {
	traj := map[string]string{}
	st := storeOf(src)
	if st == nil || f.Project == "" || len(groups) == 0 {
		return ms, traj, nil
	}
	hits, err := st.SearchTrajectory(f.Project, flatten(groups), 40)
	if err != nil || len(hits) == 0 {
		return ms, traj, err
	}
	byEvent := map[int64]string{}
	var ids []int64
	for _, h := range hits {
		body := trajectory.SearchText([]byte(h.Record))
		if grade(groups, text.Terms(body)) == 0 {
			continue
		}
		if _, ok := byEvent[h.EventID]; !ok {
			ids = append(ids, h.EventID)
		}
		byEvent[h.EventID] = strings.TrimSpace(byEvent[h.EventID] + " " + body)
	}
	cited, err := st.MemoriesCitingEvents(ids)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, m := range ms {
		seen[m.ID] = true
	}
	for _, m := range cited {
		if !visible(m, f) {
			continue
		}
		if !seen[m.ID] {
			seen[m.ID] = true
			ms = append(ms, m)
		}
	}
	// A memory can cite more than one matching record. Keep every matched text.
	evidence, err := evidenceText(st, byEvent)
	if err != nil {
		return nil, nil, err
	}
	for id, body := range evidence {
		if seen[id] {
			traj[id] = body
		}
	}
	return ms, traj, nil
}

// evidenceText maps memory id to the trajectory text of the events it cites.
func evidenceText(st *store.Store, byEvent map[int64]string) (map[string]string, error) {
	out := map[string]string{}
	if len(byEvent) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(byEvent))
	for id := range byEvent {
		ids = append(ids, id)
	}
	// MemoriesCitingEvents does not say which event cited which memory.
	// Read the evidence pairs for just these events.
	rows, err := st.EvidencePairs(ids)
	if err != nil {
		return nil, err
	}
	for _, pair := range rows {
		body := byEvent[pair.EventID]
		if body == "" {
			continue
		}
		out[pair.MemoryID] = strings.TrimSpace(out[pair.MemoryID] + " " + body)
	}
	return out, nil
}

func visible(m model.Memory, f store.ListFilter) bool {
	if len(f.Statuses) > 0 {
		ok := false
		for _, s := range f.Statuses {
			if m.Status == s {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.Project == "" {
		return true
	}
	if m.ScopeKind == model.Global || m.Scope == f.Project {
		return true
	}
	return m.ScopeKind == model.File && strings.HasPrefix(m.Scope, f.Project+"/")
}

func storeOf(src Source) *store.Store {
	switch s := src.(type) {
	case StoreSource:
		return s.Store
	case IndexSource:
		return s.Store
	default:
		return nil
	}
}

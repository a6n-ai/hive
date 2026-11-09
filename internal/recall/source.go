package recall

import (
	"slices"
	"strings"

	"github.com/a6n-ai/hive/index"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/model"
)

// Source supplies candidate memories. Terms nil means list by filter; otherwise
// the candidates are the best matches for the terms.
type Source interface {
	Candidates(terms []string, f store.ListFilter, limit int) ([]model.Memory, error)
}

// StoreSource reads candidates from the store (FTS5 or a list query).
type StoreSource struct{ Store *store.Store }

func (s StoreSource) Candidates(terms []string, f store.ListFilter, limit int) ([]model.Memory, error) {
	if terms == nil {
		return s.Store.ListMemories(f)
	}
	ms, _, err := s.Store.SearchFTS(terms, f, limit)
	return ms, err
}

// IndexSource reads prompt candidates from an in-memory index and loads the
// full memories from the store. Listing and key lookups stay on the store.
type IndexSource struct {
	Idx   *index.Index
	Store *store.Store
}

func (s IndexSource) Candidates(terms []string, f store.ListFilter, limit int) ([]model.Memory, error) {
	if terms == nil || f.Key != "" {
		return StoreSource{s.Store}.Candidates(terms, f, limit)
	}
	hits, err := s.Idx.Search(index.Query{Text: strings.Join(terms, " "), K: limit, AnyOf: s.anyOf(f)})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	ms, err := s.Store.MemoriesByID(ids)
	if err != nil {
		return nil, err
	}
	// The index can lag the store, so a memory forgotten or superseded elsewhere may still be indexed as active.
	return slices.DeleteFunc(ms, func(m model.Memory) bool { return !matches(m, f) }), nil
}

// matches re-checks the status and type filters on the rows the index returned.
func matches(m model.Memory, f store.ListFilter) bool {
	return (len(f.Statuses) == 0 || slices.Contains(f.Statuses, m.Status)) &&
		(len(f.Types) == 0 || slices.Contains(f.Types, m.Type))
}

// anyOf translates a store filter into tag groups that must all match. A
// project matches its own memories, global ones, and files under it.
func (s IndexSource) anyOf(f store.ListFilter) [][]string {
	var groups [][]string
	if len(f.Types) > 0 {
		var g []string
		for _, t := range f.Types {
			g = append(g, "type:"+string(t))
		}
		groups = append(groups, g)
	}
	if len(f.Statuses) > 0 {
		var g []string
		for _, st := range f.Statuses {
			g = append(g, "status:"+string(st))
		}
		groups = append(groups, g)
	}
	if f.Project != "" {
		g := append([]string{"project:" + f.Project, "scope:global"}, s.Idx.TagsWithPrefix("file:"+f.Project+"/")...)
		groups = append(groups, g)
	}
	return groups
}

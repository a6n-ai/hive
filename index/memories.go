package index

import (
	"strings"

	"github.com/a6n-ai/hive/internal/text"
	"github.com/a6n-ai/hive/model"
)

// MemoryFields are the text fields a memory is indexed by. They weigh the same
// as the FTS5 columns, so both paths rank alike.
var MemoryFields = []Field{{Name: "text", Weight: 1.0}, {Name: "key", Weight: 1.0}}

// MemoryDoc is the index document for m. Scope tags let a recall filter be
// answered from bitmaps: project:<p> for project memories, scope:global, and
// file:<path> for file memories.
func MemoryDoc(m model.Memory) Doc {
	tags := []string{"type:" + string(m.Type), "status:" + string(m.Status)}
	switch m.ScopeKind {
	case model.Global:
		tags = append(tags, "scope:global")
	case model.ProjectScope:
		tags = append(tags, "project:"+m.Scope)
	case model.File:
		tags = append(tags, "file:"+m.Scope)
	}
	// Distinct terms, as the FTS5 terms column stores them, so a repeated word counts once.
	return Doc{ID: m.ID, Fields: map[string]string{"text": strings.Join(text.Terms(m.Text), " "), "key": m.Key}, Tags: tags}
}

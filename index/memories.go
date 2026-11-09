package index

import "github.com/a6n-ai/hive/model"

// MemoryFields are the text fields a memory is indexed by.
var MemoryFields = []Field{{Name: "text", Weight: 1.0}, {Name: "key", Weight: 0.5}}

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
	return Doc{ID: m.ID, Fields: map[string]string{"text": m.Text, "key": m.Key}, Tags: tags}
}

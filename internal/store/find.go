package store

import "github.com/a6n-ai/inscribe/model"

// FindMemory returns the memory for a (type, scope_kind, scope, key), or nil when absent.
func (s *Store) FindMemory(typ model.MemoryType, sk model.ScopeKind, scope, key string) (*model.Memory, error) {
	m, err := scanMemory(s.db.QueryRow(memorySelect+` WHERE type=? AND scope_kind=? AND scope=? AND key=?`,
		string(typ), string(sk), scope, key))
	return absentIsNil(m, err)
}

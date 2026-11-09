package consolidate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/a6n-ai/hive/model"
)

const maxTextRunes = 500

var (
	memoryTypes = []model.MemoryType{model.Project, model.Command, model.Fix, model.Cochange, model.Preference,
		model.Decision, model.SkillUse, model.SkillFlow, model.Note}
	scopeKinds = []model.ScopeKind{model.Global, model.ProjectScope, model.File}
)

// validator holds what an op may refer to: the run's memories and events, and
// the memories earlier valid ops already changed.
type validator struct {
	mems    map[string]model.Memory
	events  map[int64]bool
	touched map[string]bool
	find    Finder
}

// Finder looks up a memory by its key; nil means no memory has that key.
type Finder func(model.MemoryType, model.ScopeKind, string, string) (*model.Memory, error)

// Validate splits ops into those that may apply and those that may not. Memory
// ids must come from the input, evidence must cite input events, no memory may
// change twice in a run, add text is scrubbed again and kept short, and an add
// may not take a key that a memory outside the input already holds.
func Validate(ops []Op, in Input, scrub func(string) string, find Finder) (valid []Op, rejected []Rejected) {
	v := validator{mems: map[string]model.Memory{}, events: map[int64]bool{}, touched: map[string]bool{}, find: find}
	for _, m := range in.Memories {
		v.mems[m.ID] = m
	}
	for _, e := range in.Events {
		v.events[e.ID] = true
	}
	for i, op := range ops {
		touches, reason := v.check(i, &op, scrub)
		if reason == "" {
			reason = v.claim(touches)
		}
		if reason != "" {
			rejected = append(rejected, Rejected{Op: op, Reason: reason})
			continue
		}
		valid = append(valid, op)
	}
	return valid, rejected
}

// check returns the existing memories an op changes, or the reason it is invalid.
func (v *validator) check(i int, op *Op, scrub func(string) string) ([]string, string) {
	if i >= maxOps {
		return nil, fmt.Sprintf("over %d ops", maxOps)
	}
	switch op.Op {
	case "merge":
		return v.merge(op)
	case "promote":
		if op.ScopeKind != "" && !slices.Contains(scopeKinds, model.ScopeKind(op.ScopeKind)) {
			return nil, "unknown scope_kind"
		}
		return []string{op.ID}, ""
	case "reject", "stale":
		return []string{op.ID}, ""
	case "supersede":
		if op.Old == op.New {
			return nil, "supersede needs two different memories"
		}
		return []string{op.Old, op.New}, ""
	case "add":
		return v.add(op, scrub)
	}
	return nil, "unknown op " + strconv.Quote(op.Op)
}

func (v *validator) merge(op *Op) ([]string, string) {
	if op.Keep == "" || len(op.Drop) == 0 {
		return nil, "merge needs keep and drop"
	}
	if slices.Contains(op.Drop, op.Keep) {
		return nil, "keep is also in drop"
	}
	keep, ok := v.mems[op.Keep]
	if !ok {
		return nil, unknown(op.Keep)
	}
	touches := []string{op.Keep}
	for _, id := range op.Drop {
		d, ok := v.mems[id]
		if !ok {
			return nil, unknown(id)
		}
		if d.Type != keep.Type {
			return nil, "merge across types"
		}
		touches = append(touches, id)
	}
	return touches, ""
}

// add checks an added memory, scrubs its text and body in place, and returns
// the existing memory it changes, if any. An existing memory must be in the input.
func (v *validator) add(op *Op, scrub func(string) string) ([]string, string) {
	switch {
	case !slices.Contains(memoryTypes, model.MemoryType(op.Type)):
		return nil, "unknown type " + strconv.Quote(op.Type)
	case !slices.Contains(scopeKinds, model.ScopeKind(op.ScopeKind)):
		return nil, "unknown scope_kind"
	case op.Key == "":
		return nil, "add needs a key"
	case len(op.Evidence) == 0:
		return nil, "add needs evidence"
	}
	for _, id := range op.Evidence {
		if !v.events[id] {
			return nil, fmt.Sprintf("evidence %d is outside the input", id)
		}
	}
	existing, err := v.find(model.MemoryType(op.Type), model.ScopeKind(op.ScopeKind), op.Scope, op.Key)
	if err != nil {
		return nil, "key lookup failed: " + err.Error()
	}
	var touches []string
	if existing != nil {
		if _, in := v.mems[existing.ID]; !in {
			return nil, "key exists; use merge or promote"
		}
		touches = []string{existing.ID}
	}
	op.Text = scrub(op.Text)
	if op.Text == "" || utf8.RuneCountInString(op.Text) > maxTextRunes {
		return nil, fmt.Sprintf("text must be 1 to %d runes", maxTextRunes)
	}
	op.Body = scrubBody(op.Body, scrub)
	return touches, ""
}

// claim marks the memories an op changes. It fails when one is not in the input,
// appears twice, or was already changed by an earlier op in this run.
func (v *validator) claim(ids []string) string {
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := v.mems[id]; !ok {
			return unknown(id)
		}
		if v.touched[id] || seen[id] {
			return fmt.Sprintf("memory %q changed twice", id)
		}
		seen[id] = true
	}
	for id := range seen {
		v.touched[id] = true
	}
	return ""
}

func unknown(id string) string { return fmt.Sprintf("unknown memory %q", id) }

// scrubBody scrubs a body through its JSON text. A body that no longer parses is dropped.
func scrubBody(body map[string]any, scrub func(string) string) map[string]any {
	if body == nil {
		return nil
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal([]byte(scrub(string(b))), &out) != nil {
		return nil
	}
	return out
}

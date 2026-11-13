package consolidate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/a6n-ai/inscribe/internal/feedback"
	"github.com/a6n-ai/inscribe/model"
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
	project string
	mems    map[string]model.Memory
	unrated map[string]bool
	events  map[int64]bool
	touched map[string]bool
	find    Finder
}

// Finder looks up a memory by its key; nil means no memory has that key.
type Finder func(model.MemoryType, model.ScopeKind, string, string) (*model.Memory, error)

// Validate splits ops into those that may apply and those that may not. Memory
// ids must come from the input, evidence must cite input events, no memory may
// change twice in a run, add text is scrubbed again and kept short, and an add
// may not take a key that a memory outside the input already holds. A rate op
// may name only an unrated memory.
func Validate(ops []Op, in Input, scrub func(string) string, find Finder) (valid []Op, rejected []Rejected) {
	v := validator{project: in.Project, mems: map[string]model.Memory{}, unrated: map[string]bool{},
		events: map[int64]bool{}, touched: map[string]bool{}, find: find}
	for _, m := range in.Memories {
		v.mems[m.ID] = m
	}
	for _, m := range in.Unrated {
		v.mems[m.ID] = m
		v.unrated[m.ID] = true
	}
	for _, e := range in.Events {
		v.events[e.ID] = true
	}
	for i, op := range ops {
		touches, reason := v.check(i, &op, scrub)
		if reason == "" {
			reason = v.guard(op, touches)
		}
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
		return v.promote(op)
	case "reject", "stale":
		return []string{op.ID}, ""
	case "supersede":
		if op.Old == op.New {
			return nil, "supersede needs two different memories"
		}
		return []string{op.Old, op.New}, ""
	case "add":
		return v.add(op, scrub)
	case "rate":
		return v.rate(op)
	}
	return nil, "unknown op " + strconv.Quote(op.Op)
}

// scopeRank orders scopes from widest to narrowest; a promotion may only narrow.
var scopeRank = map[model.ScopeKind]int{model.Global: 0, model.ProjectScope: 1, model.File: 2}

// promote needs a candidate. Its scope may only narrow, and a narrower scope must stay in the session's project.
func (v *validator) promote(op *Op) ([]string, string) {
	m, ok := v.mems[op.ID]
	if !ok {
		return nil, unknown(op.ID)
	}
	if m.Status != model.Candidate {
		return nil, "only a candidate can be promoted"
	}
	kind, scope := m.ScopeKind, m.Scope
	if op.ScopeKind != "" {
		kind = model.ScopeKind(op.ScopeKind)
	}
	if op.Scope != "" {
		scope = op.Scope
	}
	if _, known := scopeRank[kind]; !known {
		return nil, "unknown scope_kind"
	}
	if scopeRank[kind] < scopeRank[m.ScopeKind] {
		return nil, "scope may only narrow"
	}
	changed := kind != m.ScopeKind || scope != m.Scope
	if changed && kind != model.Global && !within(scope, v.project) {
		return nil, "scope must be inside the session's project"
	}
	return []string{op.ID}, ""
}

// within reports whether path is project or lies under it. An empty project matches nothing.
func within(path, project string) bool {
	return project != "" && (path == project || strings.HasPrefix(path, project+"/"))
}

// rate needs an unrated memory and answers that feedback.Label accepts.
func (v *validator) rate(op *Op) ([]string, string) {
	if !v.unrated[op.ID] {
		return nil, "rate needs a memory the session has not rated: " + strconv.Quote(op.ID)
	}
	if _, _, _, _, err := feedback.Label(op.review(), feedback.ReviewScale, "review"); err != nil {
		return nil, err.Error()
	}
	return []string{op.ID}, ""
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
	if model.ScopeKind(op.ScopeKind) == model.Global {
		return nil, "add may not be global"
	}
	if !within(op.Scope, v.project) {
		return nil, "scope must be inside the session's project"
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
	op.ID = "" // only the validator sets this: it names the memory an add updates
	if existing != nil {
		if _, in := v.mems[existing.ID]; !in {
			return nil, "key exists; use merge or promote"
		}
		touches = []string{existing.ID}
		op.ID = existing.ID
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

// guard limits what an op may change: a memory in the session's scope, and a
// global memory only by promote or reject while it is a candidate, or by rate.
func (v *validator) guard(op Op, ids []string) string {
	for _, id := range ids {
		m, ok := v.mems[id]
		if !ok {
			continue // claim reports unknown memories
		}
		if !inScope(m, v.project) {
			return "memory is outside the session's project"
		}
		if m.ScopeKind == model.Global && op.Op != "rate" && (m.Status != model.Candidate || (op.Op != "promote" && op.Op != "reject")) {
			return "a global memory changes only by promote or reject while a candidate"
		}
	}
	return ""
}

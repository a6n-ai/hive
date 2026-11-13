// Package consolidate lets the user's own agent review a session's memories:
// it proposes validated, audited, undoable changes to them.
package consolidate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/a6n-ai/inscribe/internal/feedback"
)

// maxOps is the most ops one run may propose; later ones are rejected.
const maxOps = 50

// Op is one proposed change. Only the fields its Op names are used:
// merge{keep, drop}, promote{id, scope_kind, scope}, reject{id}, stale{id},
// add{type, scope_kind, scope, key, text, body, evidence}, supersede{old, new},
// rate{id, used, helpful}.
type Op struct {
	Op        string         `json:"op"`
	ID        string         `json:"id,omitempty"`
	Keep      string         `json:"keep,omitempty"`
	Drop      []string       `json:"drop,omitempty"`
	ScopeKind string         `json:"scope_kind,omitempty"`
	Scope     string         `json:"scope,omitempty"`
	Type      string         `json:"type,omitempty"`
	Key       string         `json:"key,omitempty"`
	Text      string         `json:"text,omitempty"`
	Body      map[string]any `json:"body,omitempty"`
	Evidence  []int64        `json:"evidence,omitempty"`
	Old       string         `json:"old,omitempty"`
	New       string         `json:"new,omitempty"`
	Used      string         `json:"used,omitempty"`
	Helpful   string         `json:"helpful,omitempty"`
}

// review is the answer a rate op records. The review has no how field, so a
// "yes" is not downgraded to partly.
func (o Op) review() feedback.Answer {
	return feedback.Answer{ID: o.ID, Used: o.Used, Helpful: o.Helpful, How: "review"}
}

// Rejected is a proposed op that failed validation, with the reason.
type Rejected struct {
	Op     Op     `json:"op"`
	Reason string `json:"reason"`
}

type opsDoc struct {
	Ops []Op `json:"ops"`
}

// ParseOps reads the first JSON object in out, either bare or inside one
// ```json fence.
func ParseOps(out string) ([]Op, error) {
	var doc opsDoc
	if err := json.NewDecoder(strings.NewReader(unfence(out))).Decode(&doc); err != nil {
		return nil, fmt.Errorf("consolidate: agent output is not an ops object: %w", err)
	}
	if doc.Ops == nil {
		return nil, errors.New("consolidate: agent output has no ops array")
	}
	return doc.Ops, nil
}

// unfence strips one surrounding ``` fence, including its language tag.
func unfence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = s[3:]
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	if end := strings.LastIndex(s, "```"); end >= 0 {
		s = s[:end]
	}
	return strings.TrimSpace(s)
}

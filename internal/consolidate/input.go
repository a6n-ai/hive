package consolidate

import (
	"strings"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

const (
	maxEvents      = 500 // newest events read per run
	ftsMemories    = 30  // memories found by term search
	ftsTerms       = 50  // terms searched
	eventOverhead  = 40  // approximate JSON characters around an event's fields
	memoryOverhead = 120 // approximate JSON characters around a memory's fields
)

// InputEvent is one event as the consolidation agent sees it: scrubbed, one line.
type InputEvent struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
	Tool string `json:"tool,omitempty"`
	Text string `json:"text"`
}

// Input is what one consolidation run shows the agent.
type Input struct {
	RunID      string
	Session    string
	Agent      string
	Project    string
	Events     []InputEvent
	Memories   []model.Memory
	Unrated    []model.Memory // injected this session and never rated; the only memories a rate op may name
	MaxEventID int64
}

// BuildInput reads the session's events after fromEventID, the memories that
// cite them, and memories that match their terms. It drops the oldest events
// until the input fits budgetTokens (about 4 characters per token).
func BuildInput(st *store.Store, session string, fromEventID int64, budgetTokens int, scrub func(string) string) (Input, error) {
	evs, err := st.EventsAfter(session, fromEventID, maxEvents)
	if err != nil {
		return Input{}, err
	}
	in := Input{Session: session}
	var ids []int64
	var words strings.Builder
	for _, e := range evs {
		ie := InputEvent{ID: e.ID, Kind: string(e.Kind), Tool: e.Tool, Text: scrub(render(e))}
		in.Events = append(in.Events, ie)
		in.Agent, in.Project = e.Agent, e.Project
		ids = append(ids, e.ID)
		words.WriteString(ie.Text + " ")
		in.MaxEventID = max(in.MaxEventID, e.ID)
	}
	if in.Memories, err = relatedMemories(st, ids, words.String(), in.Project); err != nil {
		return Input{}, err
	}
	if in.Unrated, err = st.UnratedMemories(session); err != nil {
		return Input{}, err
	}
	trimToBudget(&in, budgetTokens)
	return in, nil
}

// render makes one line of an event's useful fields.
func render(e model.Event) string {
	var parts []string
	for _, k := range []string{"command", "path", "skill", "error", "text"} {
		if v, ok := e.Payload[k].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	if e.PromptText != "" {
		parts = append(parts, e.PromptText)
	}
	return strings.Join(strings.Fields(strings.Join(parts, " | ")), " ")
}

// relatedMemories returns the memories that cite the events, then memories that
// match the events' terms, without duplicates.
func relatedMemories(st *store.Store, eventIDs []int64, words, project string) ([]model.Memory, error) {
	cited, err := st.MemoriesCitingEvents(eventIDs)
	if err != nil {
		return nil, err
	}
	terms := text.Terms(words)
	terms = terms[:min(len(terms), ftsTerms)]
	live := store.ListFilter{Statuses: []model.Status{model.Active, model.Candidate}}
	found, _, err := st.SearchFTS(terms, live, ftsMemories)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []model.Memory
	for _, m := range append(cited, found...) {
		if !seen[m.ID] && (m.Status == model.Active || m.Status == model.Candidate) && inScope(m, project) {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out, nil
}

func trimToBudget(in *Input, budgetTokens int) {
	total := 0
	for _, m := range in.Memories {
		total += len(m.Text) + len(m.Key) + memoryOverhead
	}
	for _, e := range in.Events {
		total += eventSize(e)
	}
	for len(in.Events) > 0 && total > budgetTokens*4 {
		total -= eventSize(in.Events[0])
		in.Events = in.Events[1:]
	}
}

func eventSize(e InputEvent) int { return len(e.Text) + len(e.Kind) + len(e.Tool) + eventOverhead }

// inScope reports whether a memory may be shown to a session in project: a global
// memory, one scoped to the project, or a file under it.
func inScope(m model.Memory, project string) bool {
	switch m.ScopeKind {
	case model.Global:
		return true
	case model.ProjectScope:
		return m.Scope == project
	case model.File:
		return within(m.Scope, project)
	}
	return false
}

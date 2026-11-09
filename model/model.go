// Package model holds the types shared by every Hive package. It imports
// nothing from Hive so that internal packages and the public API can share it.
package model

type EventKind string

const (
	SessionStart  EventKind = "session_start"
	Prompt        EventKind = "prompt"
	PreTool       EventKind = "pre_tool"
	PostTool      EventKind = "post_tool"
	TurnEnd       EventKind = "turn_end"
	SessionEnd    EventKind = "session_end"
	SkillLoaded   EventKind = "skill_loaded"
	CommandResult EventKind = "command_result"
	Correction    EventKind = "correction"
	// Remembered is written by `hive remember`; payload {text, type, scope}.
	Remembered EventKind = "remembered"
)

type Event struct {
	ID         int64
	TS         int64 // unix ms
	Agent      string
	Session    string
	Project    string
	Kind       EventKind
	Tool       string
	Payload    map[string]any
	PromptText string
	Digest     string
}

type MemoryType string

const (
	Project    MemoryType = "project"
	Command    MemoryType = "command"
	Fix        MemoryType = "fix"
	Cochange   MemoryType = "cochange"
	Preference MemoryType = "preference"
	Decision   MemoryType = "decision"
	SkillUse   MemoryType = "skill_use"
	SkillFlow  MemoryType = "skill_flow"
	Note       MemoryType = "note"
)

type ScopeKind string

const (
	Global       ScopeKind = "global"
	ProjectScope ScopeKind = "project"
	File         ScopeKind = "file"
)

type Status string

const (
	Active     Status = "active"
	Candidate  Status = "candidate"
	Superseded Status = "superseded"
	Stale      Status = "stale"
	Forgotten  Status = "forgotten"
)

type Memory struct {
	ID           string
	Type         MemoryType
	ScopeKind    ScopeKind
	Scope        string
	Key          string
	Body         map[string]any
	Text         string
	Count        int
	Confidence   float64
	Status       Status
	SupersededBy string
	FirstSeen    int64
	LastSeen     int64
	LastUsed     int64
}

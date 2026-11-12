package recall

import (
	"regexp"
	"strings"

	"github.com/a6n-ai/inscribe/internal/text"
	"github.com/a6n-ai/inscribe/model"
)

// Facets is a prompt split into the chunks we store on a memory: the agent,
// the memory type, command spans, and path-like tokens. The remaining words
// stay in the caller's term groups and still score the text.
type Facets struct {
	Agents   []string
	Types    []model.MemoryType
	Commands [][][]string // each command span, as term groups
	Paths    []string
}

var (
	tokenRe = regexp.MustCompile(`[\p{L}\p{N}_./\\-]+`)
	tickRe  = regexp.MustCompile("`([^`]+)`")
	quoteRe = regexp.MustCompile(`"([^"]+)"`)
	pathRe  = regexp.MustCompile(`[\p{L}\p{N}_./\\-]*[/\\][\p{L}\p{N}_./\\-]+|[\p{L}\p{N}_-]+\.[A-Za-z0-9]{1,8}`)
)

var runners = map[string]bool{
	"go": true, "npm": true, "pnpm": true, "yarn": true, "bun": true,
	"git": true, "make": true, "cargo": true, "pytest": true, "python": true,
	"node": true, "docker": true,
}

var typeWords = map[string][]model.MemoryType{
	"project": {model.Project}, "command": {model.Command}, "commands": {model.Command},
	"fix": {model.Fix}, "fixes": {model.Fix}, "cochange": {model.Cochange},
	"preference": {model.Preference}, "preferences": {model.Preference},
	"decision": {model.Decision}, "decisions": {model.Decision},
	"skill": {model.SkillUse, model.SkillFlow}, "skills": {model.SkillUse, model.SkillFlow},
	"note": {model.Note}, "notes": {model.Note},
}

// chunkPrompt breaks a prompt into agent, type, command, and path chunks.
// agentName is the runtime that submitted it (claude-code, codex, gemini).
func chunkPrompt(prompt, agentName string) Facets {
	var f Facets
	seenAgent := map[string]bool{}
	addAgent := func(s string) {
		name, ok := knownAgent(s)
		if !ok || seenAgent[name] {
			return
		}
		seenAgent[name] = true
		f.Agents = append(f.Agents, name)
	}
	addAgent(agentName)
	seenType := map[model.MemoryType]bool{}
	for _, g := range text.Groups(prompt) {
		for _, term := range g {
			addAgent(term)
			for _, typ := range typeWords[term] {
				if !seenType[typ] {
					seenType[typ] = true
					f.Types = append(f.Types, typ)
				}
			}
		}
	}
	seenCmd := map[string]bool{}
	addCmd := func(span string) {
		span = strings.TrimSpace(span)
		if span == "" || seenCmd[span] {
			return
		}
		groups := text.Groups(span)
		if len(groups) == 0 {
			return
		}
		seenCmd[span] = true
		f.Commands = append(f.Commands, groups)
	}
	for _, re := range []*regexp.Regexp{tickRe, quoteRe} {
		for _, m := range re.FindAllStringSubmatch(prompt, -1) {
			addCmd(m[1])
		}
	}
	words := tokenRe.FindAllString(strings.ToLower(prompt), -1)
	for i, w := range words {
		if !runners[w] {
			continue
		}
		end := i + 1
		for end < len(words) && end < i+5 && len(text.Groups(words[end])) > 0 {
			end++
		}
		addCmd(strings.Join(words[i:end], " "))
	}
	seenPath := map[string]bool{}
	for _, p := range pathRe.FindAllString(prompt, -1) {
		p = strings.ToLower(p)
		if seenPath[p] {
			continue
		}
		seenPath[p] = true
		f.Paths = append(f.Paths, p)
	}
	return f
}

func knownAgent(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude", "claude-code", "claudecode":
		return "claude", true
	case "codex":
		return "codex", true
	case "gemini":
		return "gemini", true
	default:
		return "", false
	}
}

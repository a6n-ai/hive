package stats

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a6n-ai/inscribe/internal/config"
	"github.com/a6n-ai/inscribe/internal/store"
)

// minEstimateHits is the fewest labeled hits worth showing a credit range for.
const minEstimateHits = 30

type Rate struct {
	Hits        int            `json:"hits"`
	Misses      int            `json:"misses"`
	MissReasons map[string]int `json:"miss_reasons"`
}

type Penalty = store.Penalty

type Count struct {
	MemoryID string `json:"memory_id"`
	N        int    `json:"n"`
}

type Measured struct {
	InjectedByHook     map[string]int  `json:"injected_by_hook"`
	SessionStartTokens int             `json:"session_start_tokens"`
	SessionStartLines  int             `json:"session_start_lines"`
	Rates              map[string]Rate `json:"rates"`
	Unlabeled          int             `json:"unlabeled"`
	Shown              int             `json:"shown"`
	Wrong              int             `json:"wrong"`
	Rated              int             `json:"rated"`
	Penalized          []Penalty       `json:"penalized"`
	Asks               int             `json:"asks"`
	AsksAnswered       int             `json:"asks_answered"`
	AgentRows          int             `json:"agent_rows"`
	MemoriesUsed       int             `json:"memories_used"`
	MemoriesActive     int             `json:"memories_active"`
	Unused30d          []string        `json:"unused_30d"`
	TopInjected        []Count         `json:"top_injected"`
	ByStatus           map[string]int  `json:"by_status"`
	ByType             map[string]int  `json:"by_type"`
	DBBytes            int64           `json:"db_bytes"`
	Events             int             `json:"events"`
	Trajectory         int             `json:"trajectory"`
	NextTryFixes       int             `json:"next_try_fixes"`
	NextTryShown       int             `json:"next_try_shown"`
	Corrections        int             `json:"corrections"`
	Sessions           int             `json:"sessions"`
	Usage              store.UsageSum  `json:"usage"`
	ActiveMS           int64           `json:"active_ms"`
	UsageMissing       int             `json:"usage_missing"`
	UsageUnparsed      int             `json:"usage_unparsed"`
}

type Estimate struct {
	Hidden         bool           `json:"hidden"`
	LabeledHits    int            `json:"labeled_hits"`
	InjectedTokens int            `json:"injected_tokens"`
	LoTokens       int            `json:"lo_tokens"`
	HiTokens       int            `json:"hi_tokens"`
	BySource       map[string]int `json:"by_source"`
	Top            []MemoryCredit `json:"top"`
	Skipped        int            `json:"skipped"`
	Net            *int           `json:"net"`
}

type Report struct {
	Window   store.Window  `json:"window"`
	Measured Measured      `json:"measured"`
	Estimate Estimate      `json:"estimate"`
	Holdout  HoldoutResult `json:"holdout"`
}

var sourceOfReason = map[string]string{
	"full_command": "rule", "forgotten": "rule", "corrected": "rule", "failed": "rule",
	"agent": "agent", "agent_wrong": "agent", "review": "review", "review_wrong": "review",
}

// Build computes the report for w. now bounds the 30-day unused-memory check.
func Build(st *store.Store, w store.Window, cfg config.StatsCfg, now time.Time) (Report, error) {
	r := Report{Window: w}
	m := &r.Measured
	m.InjectedByHook, m.Rates = map[string]int{}, map[string]Rate{"rule": {}, "agent": {}, "review": {}}
	for k := range m.Rates {
		m.Rates[k] = Rate{MissReasons: map[string]int{}}
	}

	injs, err := st.InjectionsIn(w)
	if err != nil {
		return r, err
	}
	used, normalPrompts, holdoutRows := map[string]int{}, 0, 0
	for _, in := range injs {
		m.InjectedByHook[in.Hook] += in.EstTokens
		r.Estimate.InjectedTokens += in.EstTokens
		switch {
		case in.Hook == "SessionStart":
			m.SessionStartTokens += in.EstTokens
			m.SessionStartLines += in.LineCount
		case in.Hook == "UserPromptSubmit" && !in.Holdout && in.LineCount > 0:
			normalPrompts++
		}
		if in.Holdout {
			holdoutRows++
		}
		if !in.Holdout && in.Hook != "feedback_ask" {
			for _, id := range in.MemoryIDs {
				used[id]++
			}
		}
	}
	m.MemoriesUsed = len(used)
	for id, n := range used {
		m.TopInjected = append(m.TopInjected, Count{id, n})
	}
	slices.SortFunc(m.TopInjected, func(a, b Count) int { return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(a.MemoryID, b.MemoryID)) })
	m.TopInjected = m.TopInjected[:min(10, len(m.TopInjected))]

	if err := outcomes(st, w, m); err != nil {
		return r, err
	}
	if m.Asks, m.AsksAnswered, err = st.AskCounts(w); err != nil {
		return r, err
	}
	if m.ByStatus, m.ByType, err = st.MemoryCounts(); err != nil {
		return r, err
	}
	if m.Penalized, err = st.Penalized(10, w.Project); err != nil {
		return r, err
	}
	if err := unused(st, w, now, m); err != nil {
		return r, err
	}
	sc, err := st.StoreCounts(w)
	if err != nil {
		return r, err
	}
	m.Events, m.Trajectory, m.Corrections, m.Sessions = sc.Events, sc.Trajectory, sc.Corrections, sc.Sessions
	if m.NextTryFixes, m.NextTryShown, err = st.NextTryCounts(w); err != nil {
		return r, err
	}
	if m.Usage, err = st.UsageTotals(w); err != nil {
		return r, err
	}
	if m.UsageMissing, m.UsageUnparsed, err = st.UsageStatusCounts(w); err != nil {
		return r, err
	}
	idle := time.Duration(cfg.IdleMinutes) * time.Minute
	if m.ActiveMS, err = activeTotal(st, w, idle); err != nil {
		return r, err
	}
	m.DBBytes = dbBytes()

	e := &r.Estimate
	e.BySource = map[string]int{"rule": 0, "agent": 0, "review": 0}
	for _, rt := range m.Rates {
		e.LabeledHits += rt.Hits
	}
	e.Hidden = e.LabeledHits < minEstimateHits
	var expected float64
	if !e.Hidden {
		credits, skipped, err := Credit(st, w, idle)
		if err != nil {
			return r, err
		}
		e.Skipped = skipped
		for _, c := range credits {
			e.BySource[c.Source] += c.Tokens
			e.HiTokens += c.Tokens
		}
		e.LoTokens = e.BySource["rule"]
		e.Top = topCredits(credits)
		if normalPrompts > 0 {
			expected = float64(e.HiTokens) / float64(normalPrompts)
		}
	}
	if cfg.Holdout == 0 && holdoutRows == 0 {
		r.Holdout = HoldoutResult{State: "off"}
	} else if r.Holdout, err = Compare(st, w, cfg.HoldoutMinSample, expected); err != nil {
		return r, err
	}
	if r.Holdout.Verdict == "consistent" {
		net := e.HiTokens - e.InjectedTokens
		e.Net = &net
	}
	if m.Penalized == nil {
		m.Penalized = []Penalty{}
	}
	if m.TopInjected == nil {
		m.TopInjected = []Count{}
	}
	if e.Top == nil {
		e.Top = []MemoryCredit{}
	}
	return r, nil
}

func outcomes(st *store.Store, w store.Window, m *Measured) error {
	rows, err := st.OutcomeCounts(w)
	if err != nil {
		return err
	}
	for _, c := range rows {
		m.Shown += c.N
		src, known := sourceOfReason[c.Reason]
		if !known || c.Label < 0 {
			m.Unlabeled += c.N
			continue
		}
		rt := m.Rates[src]
		if c.Label == 1 {
			rt.Hits += c.N
		} else {
			rt.Misses += c.N
			rt.MissReasons[c.Reason] += c.N
		}
		m.Rates[src] = rt
		if src != "rule" {
			m.Rated += c.N
		}
		if src == "agent" {
			m.AgentRows += c.N
		}
		if strings.HasSuffix(c.Reason, "_wrong") {
			m.Wrong += c.N
		}
	}
	return nil
}

func unused(st *store.Store, w store.Window, now time.Time, m *Measured) error {
	ids, err := st.ActiveMemoryIDs(w.Project)
	if err != nil {
		return err
	}
	m.MemoriesActive = len(ids)
	recent, err := st.InjectionsIn(store.Window{From: now.UnixMilli() - 30*24*time.Hour.Milliseconds(), To: w.To, Project: w.Project})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, in := range recent {
		if !in.Holdout && in.Hook != "feedback_ask" {
			for _, id := range in.MemoryIDs {
				seen[id] = true
			}
		}
	}
	m.Unused30d = []string{}
	for _, id := range ids {
		if !seen[id] {
			m.Unused30d = append(m.Unused30d, id)
		}
	}
	return nil
}

func activeTotal(st *store.Store, w store.Window, idle time.Duration) (int64, error) {
	sessions, err := st.SessionsIn(w)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, s := range sessions {
		evs, err := st.EventsIn(s, w.From, w.To)
		if err != nil {
			return 0, err
		}
		total += ActiveMS(evs, idle)
	}
	return total, nil
}

// topCredits sums credit per memory and returns the five largest.
func topCredits(credits []MemoryCredit) []MemoryCredit {
	by := map[string]*MemoryCredit{}
	var order []string
	for _, c := range credits {
		if t := by[c.MemoryID]; t != nil {
			t.Tokens += c.Tokens
			t.ActiveMS += c.ActiveMS
			continue
		}
		c := c
		by[c.MemoryID] = &c
		order = append(order, c.MemoryID)
	}
	out := make([]MemoryCredit, 0, len(order))
	for _, id := range order {
		out = append(out, *by[id])
	}
	slices.SortFunc(out, func(a, b MemoryCredit) int {
		return cmp.Or(cmp.Compare(b.Tokens, a.Tokens), cmp.Compare(a.MemoryID, b.MemoryID))
	})
	return out[:min(5, len(out))]
}

func dbBytes() int64 {
	var n int64
	for _, f := range []string{"inscribe.db", "inscribe.db-wal"} {
		if fi, err := os.Stat(filepath.Join(config.Home(), f)); err == nil {
			n += fi.Size()
		}
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pct(n, of int) string {
	if of == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d%%)", (n*100+of/2)/of)
}

// tokf prints a possibly negative token count.
func tokf(f float64) string {
	s := FormatTokens(int(math.Round(math.Abs(f))))
	if f < 0 {
		return "-" + s
	}
	return s
}

func stokf(f float64) string {
	if f >= 0 {
		return "+" + tokf(f)
	}
	return tokf(f)
}

func bigTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	return FormatTokens(n)
}

func duration(ms int64) string {
	mins := ms / 60000
	if mins >= 60 {
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}

func size(b int64) string {
	if b < 1_000_000 {
		return fmt.Sprintf("%d KB", b/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(b)/1e6)
}

var hookOrder = []string{"SessionStart", "UserPromptSubmit", "PostToolUseFailure", "feedback_ask"}
var reasonOrder = []string{"forgotten", "corrected", "failed", "agent", "review", "agent_wrong", "review_wrong"}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func joinParts(parts []string) string {
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " · ")
}

// Text renders r as the terminal report; explain appends the model and its limits.
func Text(r Report, explain bool) string {
	var b strings.Builder
	line := func(label, format string, a ...any) { fmt.Fprintf(&b, "  %-15s%s\n", label, fmt.Sprintf(format, a...)) }
	m := r.Measured

	scope := "all projects"
	if r.Window.Project != "" {
		scope = "this project"
	}
	days := int(math.Round(float64(r.Window.To-r.Window.From) / float64(24*time.Hour.Milliseconds())))
	fmt.Fprintf(&b, "inscribe stats: %s, last %dd (%s)\n\nMeasured\n", scope, days, plural(m.Sessions, "session", "sessions"))

	var parts []string
	for _, h := range hookOrder {
		if n, ok := m.InjectedByHook[h]; ok {
			parts = append(parts, fmt.Sprintf("%s %s tok", h, FormatTokens(n)))
		}
	}
	line("injected", "%s", joinParts(parts))
	line("session-start", "%d lines, %s tok (cost only, not labeled)", m.SessionStartLines, FormatTokens(m.SessionStartTokens))

	parts = nil
	for _, k := range []string{"rule", "agent", "review"} {
		rt := m.Rates[k]
		n := rt.Hits + rt.Misses
		parts = append(parts, fmt.Sprintf("%s %d/%d%s", k, rt.Hits, n, pct(rt.Hits, n)))
	}
	parts = append(parts, fmt.Sprintf("unlabeled %d", m.Unlabeled))
	line("hits", "%s", strings.Join(parts, " · "))

	reasons := map[string]int{}
	for _, rt := range m.Rates {
		for k, n := range rt.MissReasons {
			reasons[k] += n
		}
	}
	parts = nil
	for _, k := range append(slices.Clone(reasonOrder), sortedKeys(reasons)...) {
		if n := reasons[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
			delete(reasons, k)
		}
	}
	line("misses", "%s", joinParts(parts))

	wrong := fmt.Sprintf("%d/%d rated%s", m.Wrong, m.Rated, pct(m.Wrong, m.Rated))
	if len(m.Penalized) > 0 {
		var ps []string
		for _, p := range m.Penalized {
			ps = append(ps, fmt.Sprintf("%s %g", p.MemoryID, p.Penalty))
		}
		wrong += " · penalized: " + strings.Join(ps, ", ")
	}
	line("wrong", "%s", wrong)
	line("feedback asks", "%d/%d answered · %d agent-rated rows", m.AsksAnswered, m.Asks, m.AgentRows)

	mem := fmt.Sprintf("%d/%d active used · %d unused 30d", m.MemoriesUsed, m.MemoriesActive, len(m.Unused30d))
	if len(m.TopInjected) > 0 {
		var ts []string
		for _, c := range m.TopInjected[:min(5, len(m.TopInjected))] {
			ts = append(ts, fmt.Sprintf("%s %d", c.MemoryID, c.N))
		}
		mem += " · top: " + strings.Join(ts, ", ")
	}
	line("memories", "%s", mem)

	parts = nil
	for _, k := range sortedKeys(m.ByStatus) {
		parts = append(parts, fmt.Sprintf("%s %d", k, m.ByStatus[k]))
	}
	parts = append(parts, size(m.DBBytes), fmt.Sprintf("%d events", m.Events), fmt.Sprintf("%d trajectory", m.Trajectory))
	line("store", "%s", strings.Join(parts, " · "))

	line("next try", "%d of %d shown fixes followed by a success (correlation, not cause)", m.NextTryFixes, m.NextTryShown)
	perSession := 0.0
	if m.Sessions > 0 {
		perSession = float64(m.Corrections) / float64(m.Sessions)
	}
	line("corrections", "%.1f per session", perSession)
	u := m.Usage
	line("usage", "in %s · out %s · cache read %s · cache write %s tok · active %s",
		bigTokens(u.Input), bigTokens(u.Output), bigTokens(u.CacheRead), bigTokens(u.CacheCreation), duration(m.ActiveMS))
	line("usage missing", "%d missing · %d unparsed", m.UsageMissing, m.UsageUnparsed)

	e := r.Estimate
	b.WriteString("\nEstimated\n")
	if e.Hidden {
		fmt.Fprintf(&b, "  not enough data (%d labeled hits, need %d)\n", e.LabeledHits, minEstimateHits)
	} else {
		line("injected", "%s tok (estimated, input, counted once)", FormatTokens(e.InjectedTokens))
		line("credit", "%s-%s tok (from transcript: input + output + cache creation)", FormatTokens(e.LoTokens), FormatTokens(e.HiTokens))
		line("by source", "rule %s · agent %s · review %s", FormatTokens(e.BySource["rule"]), FormatTokens(e.BySource["agent"]), FormatTokens(e.BySource["review"]))
		if len(e.Top) > 0 {
			var ts []string
			for _, c := range e.Top {
				ts = append(ts, fmt.Sprintf("%s %s tok %s", c.MemoryID, FormatTokens(c.Tokens), duration(c.ActiveMS)))
			}
			line("top", "%s", strings.Join(ts, " · "))
		}
		if e.Skipped > 0 {
			line("skipped", "%d memories (no evidence or no usage)", e.Skipped)
		}
		if e.Net != nil {
			if *e.Net >= 0 {
				line("net", "%s tok saved", FormatTokens(*e.Net))
			} else {
				line("net", "%s tok lost", FormatTokens(-*e.Net))
			}
		}
	}

	h := r.Holdout
	b.WriteString("\nHoldout\n")
	switch h.State {
	case "collecting":
		fmt.Fprintf(&b, "  collecting, %d of %d holdout · %d of %d normal\n", h.Holdout.N, h.MinSample, h.Normal.N, h.MinSample)
	case "compared":
		fmt.Fprintf(&b, "  holdout %d · normal %d · tokens %s (95%% %s to %s) · expected %s · %s\n",
			h.Holdout.N, h.Normal.N, stokf(h.Diff), tokf(h.Lo), tokf(h.Hi), stokf(h.Expected), h.Verdict)
		for _, g := range []struct {
			name string
			s    GroupStats
		}{{"holdout", h.Holdout}, {"normal", h.Normal}} {
			line(g.name, "failed %.2f · corrections %.2f · would-be-hit %d%%", g.s.FailedPerPrompt, g.s.CorrectionsPerPrompt, int(math.Round(g.s.WouldBeHitRate*100)))
		}
	default:
		b.WriteString("  off\n")
	}

	if explain {
		b.WriteString(explainText)
	}
	return b.String()
}

const explainText = `
How to read the estimate
  Credit is the tokens (input + output + cache creation) and active time of the session window that first discovered each memory that hit. Cache reads are left out.
  Biases:
  - Overstates when the discovery window contains unrelated work. The split rule reduces this but does not remove it.
  - Understates because the command rule needs every content word of the command in the line, so many useful lines stay unlabeled. Agent and review labels close part of this gap.
  - Self-report: agent hits are credited like rule hits. The per-source range lets the reader discount them.
  - Cost is low: injected cost counts each injection once. Later turns re-read it from the prompt cache, so the true cost is higher than shown. Input, output and cache-creation tokens are also not equal in price.
  - Skips memories with no evidence (imported from claude-mem, or added by remember) and memories whose discovery session has no usage. The skipped count is printed.
  - Shared windows: credit is split among memories that hit in this report window, so the same memory can show more credit in a shorter window.
Holdout limits
  Prompts in one session are not independent, and session-start lines stay in context across holdout prompts. The interval treats prompts as independent, so it is narrower than the truth.
`

package stats

import (
	"time"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

// MemoryCredit is one memory's hit credited with its discovery window's cost.
type MemoryCredit struct {
	MemoryID, Source string
	Tokens           int
	ActiveMS         int64
}

var sourceOf = map[string]string{"full_command": "rule", "agent": "agent", "review": "review"}

// ActiveMS sums gaps between consecutive events, dropping gaps longer than idle.
func ActiveMS(events []model.Event, idle time.Duration) int64 {
	var total int64
	for i := 1; i < len(events); i++ {
		if gap := events[i].TS - events[i-1].TS; gap <= idle.Milliseconds() {
			total += gap
		}
	}
	return total
}

type window struct {
	session    string
	start, end int64
}

type windowCost struct {
	tokens   int
	activeMS int64
}

// Credit credits each hit in w with the tokens and active time of its memory's
// discovery window. Hits of one memory in one session count once. It also
// returns how many distinct memories had no usable window.
func Credit(st *store.Store, w store.Window, idle time.Duration) ([]MemoryCredit, int, error) {
	hits, err := st.HitsIn(w)
	if err != nil {
		return nil, 0, err
	}
	type key struct{ mem, session string }
	wins := map[string]*window{}
	skipped := map[string]bool{}
	costs := map[window]*windowCost{}
	seen := map[key]bool{}
	var chosen []picked
	for _, h := range hits {
		source, ok := sourceOf[h.Reason]
		if !ok || skipped[h.MemoryID] || seen[key{h.MemoryID, h.Session}] {
			continue
		}
		win, known := wins[h.MemoryID]
		if !known {
			win, err = discoveryWindow(st, h.MemoryID)
			if err != nil {
				return nil, 0, err
			}
			wins[h.MemoryID] = win
		}
		if win == nil {
			skipped[h.MemoryID] = true
			continue
		}
		if costs[*win] == nil {
			c, err := windowCostOf(st, *win, idle)
			if err != nil {
				return nil, 0, err
			}
			if c == nil {
				skipped[h.MemoryID] = true
				continue
			}
			costs[*win] = c
		}
		seen[key{h.MemoryID, h.Session}] = true
		chosen = append(chosen, picked{h.MemoryID, source, *win})
	}
	members := map[window]map[string]bool{}
	for _, p := range chosen {
		if members[p.win] == nil {
			members[p.win] = map[string]bool{}
		}
		members[p.win][p.mem] = true
	}
	out := make([]MemoryCredit, 0, len(chosen))
	for _, p := range chosen {
		c, n := costs[p.win], len(members[p.win])
		out = append(out, MemoryCredit{MemoryID: p.mem, Source: p.source, Tokens: c.tokens / n, ActiveMS: c.activeMS / int64(n)})
	}
	return out, len(skipped), nil
}

type picked struct {
	mem, source string
	win         window
}

// discoveryWindow runs from the prompt that led to the memory's earliest
// evidence to its last evidence in that session; nil when there is no evidence.
func discoveryWindow(st *store.Store, memoryID string) (*window, error) {
	evs, err := st.Evidence(memoryID)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	first := evs[0]
	win := window{session: first.Session, start: first.TS}
	for _, e := range evs {
		if e.Session == first.Session && e.TS > win.end {
			win.end = e.TS
		}
	}
	before, err := st.EventsIn(first.Session, 0, first.TS)
	if err != nil {
		return nil, err
	}
	for _, e := range before {
		if e.Kind == model.Prompt {
			win.start = e.TS
		}
	}
	return &win, nil
}

func windowCostOf(st *store.Store, w window, idle time.Duration) (*windowCost, error) {
	u, err := st.UsageInRange(w.session, w.start, w.end+1)
	if err != nil {
		return nil, err
	}
	if u == (store.UsageSum{}) {
		return nil, nil
	}
	evs, err := st.EventsIn(w.session, w.start, w.end)
	if err != nil {
		return nil, err
	}
	return &windowCost{tokens: u.Input + u.Output + u.CacheCreation, activeMS: ActiveMS(evs, idle)}, nil
}

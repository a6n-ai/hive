package consolidate

import (
	"strings"
	"testing"

	"github.com/a6n-ai/inscribe/model"
)

// Unrated memories count toward the budget like Memories, so they can push
// events out of the input.
func TestTrimToBudgetCountsUnrated(t *testing.T) {
	in := Input{
		Events:  []InputEvent{{ID: 1, Kind: "prompt", Text: strings.Repeat("x", 100)}},
		Unrated: []model.Memory{{ID: "m1"}},
	}
	trimToBudget(&in, 60)
	if len(in.Events) != 0 {
		t.Fatalf("kept %d events; the unrated memory should overflow the budget", len(in.Events))
	}
}

func TestNewestKeepsMostRecentlySeen(t *testing.T) {
	ms := []model.Memory{{ID: "old", LastSeen: 1}, {ID: "new", LastSeen: 3}, {ID: "mid", LastSeen: 2}}
	got := newest(ms, 2)
	if len(got) != 2 || got[0].ID != "new" || got[1].ID != "mid" {
		t.Fatalf("newest = %v, want [new mid]", got)
	}
}

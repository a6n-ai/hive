package stats

import (
	"fmt"
	"testing"
)

func TestDraw(t *testing.T) {
	if Draw("s", "d", 1, 0.3) != Draw("s", "d", 1, 0.3) {
		t.Fatal("Draw is not deterministic")
	}
	holdouts := 0
	for i := range 10000 {
		if Draw("s", "d", i, 0) {
			t.Fatal("share 0 drew a holdout")
		}
		if Draw(fmt.Sprintf("s%d", i), "d", i, 0.05) {
			holdouts++
		}
	}
	if holdouts < 400 || holdouts > 600 {
		t.Fatalf("share 0.05 gave %d holdouts of 10000, want 400-600", holdouts)
	}
	var yes, no int
	for i := range 100 {
		if Draw(fmt.Sprintf("s%d", i), "d", 0, 0.5) {
			yes++
		} else {
			no++
		}
	}
	if yes == 0 || no == 0 {
		t.Fatalf("one digest over 100 sessions: %d held out, %d not", yes, no)
	}
}

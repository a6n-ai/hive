package recall

import (
	"testing"

	"github.com/a6n-ai/inscribe/internal/store"
	"github.com/a6n-ai/inscribe/model"
)

const labelEnded = int64(1000)

var noForget ForgetFunc = func(string) (int64, bool, error) { return 0, false, nil }

func labelRow(t *testing.T, id int64, memID, key, shown string, f Features) store.ScoreOutcome {
	t.Helper()
	raw, err := f.marshal()
	if err != nil {
		t.Fatal(err)
	}
	return store.ScoreOutcome{ID: id, MemoryID: memID, Session: "s1", PromptDigest: "p1",
		ShownKey: key, ShownText: shown, Features: raw}
}

// liftRow is a line where the key column lifts over the text floor.
func liftRow(t *testing.T, id int64, memID, key, shown string) store.ScoreOutcome {
	return labelRow(t, id, memID, key, shown, Features{Text: 0.5, Key: 1})
}

func labelPrompt(ts int64, digest string) model.Event {
	return model.Event{TS: ts, Session: "s1", Kind: model.Prompt, Digest: digest}
}

func labelCmd(ts int64, command string, exit int) model.Event {
	return model.Event{TS: ts, Session: "s1", Kind: model.PostTool,
		Payload: map[string]any{"command": command, "exit_code": exit}}
}

func labelCorrection(ts int64, text string) model.Event {
	return model.Event{TS: ts, Session: "s1", Kind: model.Correction, Payload: map[string]any{"text": text}}
}

func checkLabel(t *testing.T, got Labeled, label int, reason string, scale float64) {
	t.Helper()
	gotLabel := -1
	if got.Label != nil {
		gotLabel = *got.Label
	}
	if gotLabel != label || got.Reason != reason || got.Scale != scale {
		t.Fatalf("row %d: got label %d reason %q scale %v, want label %d reason %q scale %v",
			got.ID, gotLabel, got.Reason, got.Scale, label, reason, scale)
	}
}

func TestContentWords(t *testing.T) {
	cases := map[string][]string{
		"go test ./internal/recall": {"test"},
		"go test ./...":             {"test"},
		"npm run build:web":         {"npm", "run", "build", "web"},
		"cat ~/notes.txt README":    {"cat", "readme"},
	}
	for cmd, want := range cases {
		got := ContentWords(cmd)
		if len(got) != len(want) {
			t.Fatalf("ContentWords(%q) = %v, want %v", cmd, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ContentWords(%q) = %v, want %v", cmd, got, want)
			}
		}
	}
}

func TestLabelHitAndSiblings(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
		liftRow(t, 3, "m3", "deploy", "ship staging"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(200, "npm run build web", 0)}
	got, ok, err := LabelGroup(rows, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], 1, "full_command", 1)
	checkLabel(t, got[1], 0, "sibling", 0.5)
	checkLabel(t, got[2], 0, "sibling", 0.5)
}

func TestLabelFailedMakesNoSiblings(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
		liftRow(t, 3, "m3", "deploy", "ship staging"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(200, "npm run build web", 1)}
	got, ok, err := LabelGroup(rows, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], 0, "failed", 1)
	checkLabel(t, got[1], -1, "closed_unlabeled", 0)
	checkLabel(t, got[2], -1, "closed_unlabeled", 0)
}

func TestLabelCorrectionBeatsHit(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
		liftRow(t, 3, "m3", "deploy", "ship staging"),
	}
	events := []model.Event{
		labelPrompt(100, "p1"),
		labelCmd(200, "npm run build web", 0),
		labelCorrection(300, "npm run build web"),
	}
	got, ok, err := LabelGroup(rows, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], 0, "corrected", 1)
	checkLabel(t, got[1], -1, "closed_unlabeled", 0)
	checkLabel(t, got[2], -1, "closed_unlabeled", 0)
}

func TestLabelForgottenBeforeEnd(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
	}
	before := func(id string) (int64, bool, error) {
		if id == "m1" {
			return 500, true, nil
		}
		return 0, false, nil
	}
	got, ok, err := LabelGroup(rows, []model.Event{labelPrompt(100, "p1")}, labelEnded, before, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], 0, "forgotten", 1)
	checkLabel(t, got[1], -1, "closed_unlabeled", 0)

	after := func(id string) (int64, bool, error) {
		if id == "m1" {
			return 1500, true, nil
		}
		return 0, false, nil
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(200, "npm run build web", 0)}
	got, ok, err = LabelGroup(rows, events, labelEnded, after, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], 1, "full_command", 1)
}

func TestLabelEachGroupClosesOnOwnCommand(t *testing.T) {
	group1 := []store.ScoreOutcome{liftRow(t, 1, "m1", "build", "npm run build web")}
	group2 := []store.ScoreOutcome{liftRow(t, 2, "m1", "build", "npm run build web")}
	group2[0].PromptDigest = "p2"
	events := []model.Event{
		labelPrompt(100, "p1"),
		labelCmd(200, "npm run build web", 0),
		labelPrompt(300, "p2"),
		labelCmd(400, "npm run build web", 1),
	}
	got1, ok, err := LabelGroup(group1, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got1[0], 1, "full_command", 1)
	got2, ok, err := LabelGroup(group2, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got2[0], 0, "failed", 1)
}

func TestLabelEventsAfterEndIgnored(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(1200, "npm run build web", 0)}
	got, ok, err := LabelGroup(rows, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], -1, "closed_unlabeled", 0)
	checkLabel(t, got[1], -1, "closed_unlabeled", 0)
}

func TestLabelNoLift(t *testing.T) {
	rows := []store.ScoreOutcome{
		labelRow(t, 1, "m1", "build", "npm run build web", Features{Text: 1}),
		liftRow(t, 2, "m2", "build", "npm run build web"),
		liftRow(t, 3, "m3", "tests", "lint notes"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(200, "npm run build web", 0)}
	got, ok, err := LabelGroup(rows, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], -1, "no_lift", 0)
	checkLabel(t, got[1], 1, "full_command", 1)
	checkLabel(t, got[2], 0, "sibling", 1)
}

func TestLabelMissingPromptEvent(t *testing.T) {
	rows := []store.ScoreOutcome{liftRow(t, 1, "m1", "build", "npm run build web")}
	events := []model.Event{labelPrompt(100, "other"), labelCmd(200, "npm run build web", 0)}
	got, ok, err := LabelGroup(rows, events, labelEnded, noForget, DefaultWeights())
	if err != nil {
		t.Fatal(err)
	}
	if ok || got != nil {
		t.Fatalf("ok=%v got=%v, want the group left open", ok, got)
	}
}

func TestLabelOneWordCommandNeverHits(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "make build"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(200, "make", 0)}
	got, ok, err := LabelGroup(rows, events, labelEnded, noForget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	checkLabel(t, got[0], -1, "closed_unlabeled", 0)
	checkLabel(t, got[1], -1, "closed_unlabeled", 0)
}

func labelOK(t *testing.T, rows []store.ScoreOutcome, events []model.Event, forget ForgetFunc) []Labeled {
	t.Helper()
	got, ok, err := LabelGroup(rows, events, labelEnded, forget, DefaultWeights())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	return got
}

func TestLabelCorrectionScaleSharedAcrossLines(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "deploy", "ship staging"),
		liftRow(t, 3, "m3", "tests", "lint notes"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCorrection(300, "npm run build web ship staging")}
	got := labelOK(t, rows, events, noForget)
	checkLabel(t, got[0], 0, "corrected", 0.5)
	checkLabel(t, got[1], 0, "corrected", 0.5)
	checkLabel(t, got[2], -1, "closed_unlabeled", 0)
}

func TestLabelLargerCorrectionScaleWins(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "deploy", "ship staging"),
	}
	events := []model.Event{
		labelPrompt(100, "p1"),
		labelCorrection(300, "npm run build web ship staging"),
		labelCorrection(400, "npm run build web"),
	}
	got := labelOK(t, rows, events, noForget)
	checkLabel(t, got[0], 0, "corrected", 1)
	checkLabel(t, got[1], 0, "corrected", 0.5)
}

func TestLabelHitScaleSharedAcrossLines(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "web", "npm run build web"),
		liftRow(t, 3, "m3", "tests", "lint notes"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(200, "npm run build web", 0)}
	got := labelOK(t, rows, events, noForget)
	checkLabel(t, got[0], 1, "full_command", 0.5)
	checkLabel(t, got[1], 1, "full_command", 0.5)
	checkLabel(t, got[2], 0, "sibling", 1)
}

func TestLabelSiblingScaleSharedAcrossLines(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
		liftRow(t, 3, "m3", "deploy", "ship staging"),
		liftRow(t, 4, "m4", "docs", "write readme"),
	}
	events := []model.Event{labelPrompt(100, "p1"), labelCmd(200, "npm run build web", 0)}
	got := labelOK(t, rows, events, noForget)
	checkLabel(t, got[0], 1, "full_command", 1)
	for _, i := range []int{1, 2, 3} {
		checkLabel(t, got[i], 0, "sibling", 1.0/3)
	}
}

func TestLabelForgottenBeatsCorrectedAndHit(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
	}
	gone := func(id string) (int64, bool, error) { return 500, id == "m1", nil }
	events := []model.Event{
		labelPrompt(100, "p1"),
		labelCmd(200, "npm run build web", 0),
		labelCorrection(300, "npm run build web"),
	}
	got := labelOK(t, rows, events, gone)
	checkLabel(t, got[0], 0, "forgotten", 1)
}

func TestLabelCorrectedBeatsFailed(t *testing.T) {
	rows := []store.ScoreOutcome{
		liftRow(t, 1, "m1", "build", "npm run build web"),
		liftRow(t, 2, "m2", "tests", "lint notes"),
	}
	events := []model.Event{
		labelPrompt(100, "p1"),
		labelCmd(200, "npm run build web", 1),
		labelCorrection(300, "npm run build web"),
	}
	got := labelOK(t, rows, events, noForget)
	checkLabel(t, got[0], 0, "corrected", 1)
}

func TestLabelCorruptFeaturesWithMissingPromptStaysOpen(t *testing.T) {
	rows := []store.ScoreOutcome{{ID: 1, MemoryID: "m1", Session: "s1", PromptDigest: "p1", Features: "not json"}}
	got, ok, err := LabelGroup(rows, []model.Event{labelPrompt(100, "other")}, labelEnded, noForget, DefaultWeights())
	if err != nil || ok || got != nil {
		t.Fatalf("got=%v ok=%v err=%v, want the group left open without an error", got, ok, err)
	}
}

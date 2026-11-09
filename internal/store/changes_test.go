package store

import (
	"slices"
	"testing"
)

func TestMemoriesByIDKeepsRequestedOrder(t *testing.T) {
	st, _ := openTemp(t)
	a := upsertOne(t, st, changeTestMemory("a"))
	b := upsertOne(t, st, changeTestMemory("b"))

	ms, err := st.MemoriesByID([]string{b, "missing", a})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	if !slices.Equal(got, []string{b, a}) {
		t.Fatalf("MemoriesByID = %v, want [%s %s]", got, b, a)
	}
}

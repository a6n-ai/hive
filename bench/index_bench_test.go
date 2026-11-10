package bench

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a6n-ai/hive/index"
	"github.com/a6n-ai/hive/internal/recall"
	"github.com/a6n-ai/hive/internal/store"
	"github.com/a6n-ai/hive/internal/text"
	"github.com/a6n-ai/hive/model"
)

const (
	skillDocs       = 1_000
	skillDim        = 256
	skillP99Target  = time.Millisecond
	recallMemories  = 1_000_000
	recallProjects  = 50
	recallP99Target = 5 * time.Millisecond
	vocabSize       = 20_000
	samples         = 2_000 // individually timed queries per benchmark
)

var (
	skillFields = []index.Field{{Name: "name", Weight: 2}, {Name: "description", Weight: 1}, {Name: "triggers", Weight: 1.5}}
	hotWords    = []string{"deploy", "staging", "service"}

	skillOnce    sync.Once
	skillIdx     *index.Index
	skillQueries []index.Query

	recallOnce    sync.Once
	recallSrc     recall.IndexSource
	recallPrompts []string
)

func requireIndexBench(b *testing.B) {
	b.Helper()
	if os.Getenv("HIVE_BENCH") != "1" {
		b.Skip("set HIVE_BENCH=1 to run the index benchmarks")
	}
}

// BenchmarkSkillSearch: skill search over 1,000 documents with 256-dim vectors.
// The index is built once; each of samples queries is timed on its own, over
// varied queries. Run with -benchtime=1x.
func BenchmarkSkillSearch(b *testing.B) {
	requireIndexBench(b)
	skillOnce.Do(func() { buildSkillIndex(b) })
	b.StopTimer()
	durs := make([]time.Duration, samples)
	for i := range samples {
		q := skillQueries[i%len(skillQueries)]
		start := time.Now()
		if _, err := skillIdx.Search(q); err != nil {
			b.Fatal(err)
		}
		durs[i] = time.Since(start)
	}
	reportDistribution(b, durs, skillP99Target)
}

// BenchmarkRecallAtScale: recall candidates through the index over 1,000,000
// memories in 50 projects, with a project filter. Each sample is one
// IndexSource.Candidates call (index search plus loading the memories from the
// store) over varied prompts and projects. The store is seeded once and cached
// under .cache. Run with -benchtime=1x.
func BenchmarkRecallAtScale(b *testing.B) {
	requireIndexBench(b)
	recallOnce.Do(func() { buildRecall(b, filepath.Join(".cache", "index-seed")) })
	b.StopTimer()
	durs := make([]time.Duration, samples)
	for i := range samples {
		filter := store.ListFilter{Project: recallProject(i), Statuses: []model.Status{model.Active}}
		terms := text.Terms(recallPrompts[i%len(recallPrompts)])
		start := time.Now()
		ms, err := recallSrc.Candidates(terms, filter, 50)
		durs[i] = time.Since(start)
		if err != nil {
			b.Fatal(err)
		}
		if len(ms) == 0 {
			b.Fatalf("no candidates for %s", filter.Project)
		}
	}
	reportDistribution(b, durs, recallP99Target)
}

// BenchmarkBuild1M times building the index from 1,000,000 stored memories.
// Reading them from the store is outside the timed region.
func BenchmarkBuild1M(b *testing.B) {
	requireIndexBench(b)
	st := seedRecallStore(b, filepath.Join(".cache", "index-seed"))
	ms, err := st.ListMemories(store.ListFilter{Statuses: []model.Status{model.Active}})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		start := time.Now()
		ix := index.New(index.MemoryFields, 0)
		for _, m := range ms {
			if err := ix.Upsert(index.MemoryDoc(m)); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(time.Since(start).Seconds(), "build-s")
	}
}

// reportDistribution reports p50 and p99 and fails when p99 reaches the target
// on a strict run.
func reportDistribution(b *testing.B, durs []time.Duration, target time.Duration) {
	b.Helper()
	s := slices.Clone(durs)
	slices.Sort(s)
	p50 := s[int(math.Ceil(0.50*float64(len(s))))-1]
	p99 := s[int(math.Ceil(0.99*float64(len(s))))-1]
	b.ReportMetric(float64(p50)/float64(time.Millisecond), "p50-ms")
	b.ReportMetric(float64(p99)/float64(time.Millisecond), "p99-ms")
	b.Logf("p50 %v, p99 %v over %d queries", p50, p99, len(s))
	if p99 >= target {
		msg := fmt.Sprintf("p99 %v is not below the %v target", p99, target)
		if targetFails(p99, target, os.Getenv("HIVE_BENCH_STRICT") == "1") {
			b.Error(msg)
		} else {
			b.Log(msg + " (set HIVE_BENCH_STRICT=1 to fail on it)")
		}
	}
}

func buildSkillIndex(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 2))
	skillIdx = index.New(skillFields, skillDim)
	for i := range skillDocs {
		d := index.Doc{ID: fmt.Sprintf("skill-%d", i), Vector: unitVector(rng, skillDim),
			Fields: map[string]string{"name": words(rng, 2), "description": words(rng, 12), "triggers": words(rng, 4)}}
		if err := skillIdx.Upsert(d); err != nil {
			b.Fatal(err)
		}
	}
	for range 100 {
		skillQueries = append(skillQueries, index.Query{Text: words(rng, 3), Vector: unitVector(rng, skillDim), K: 10,
			Fusion: index.Fusion{Mode: index.Convex, Alpha: 0.5}})
	}
}

func buildRecall(b *testing.B, home string) {
	st := seedRecallStore(b, home)
	recallSrc = recall.IndexSource{Idx: buildIndex(b, st), Store: st}
	rng := rand.New(rand.NewPCG(5, 6))
	recallPrompts = []string{"how do we deploy the service to staging", "deploy staging", "service tests"}
	for range 100 {
		recallPrompts = append(recallPrompts, words(rng, 2)+" deploy "+words(rng, 2))
	}
}

func recallProject(i int) string { return fmt.Sprintf("/work/proj-%d", i%recallProjects) }

func unitVector(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	return v
}

// words returns n random vocabulary words, space-separated.
func words(rng *rand.Rand, n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("w%d", rng.IntN(vocabSize))
	}
	return strings.Join(out, " ")
}

// seedRecallStore fills a store with recallMemories memories once; a marker
// file skips the seeding on later runs.
func seedRecallStore(b *testing.B, home string) *store.Store {
	b.Helper()
	marker := filepath.Join(home, "seeded")
	if err := os.MkdirAll(home, 0o700); err != nil {
		b.Fatal(err)
	}
	_, statErr := os.Stat(marker)
	st, err := store.Open(filepath.Join(home, "hive.db"), false)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	if statErr == nil {
		return st
	}
	rng := rand.New(rand.NewPCG(3, 4))
	now := time.Now().UnixMilli()
	const batch = 10_000
	for base := 0; base < recallMemories; base += batch {
		if err := st.Write(context.Background(), func(tx *store.Tx) error {
			for i := base; i < base+batch; i++ {
				text := words(rng, 8)
				if i%97 == 0 { // 97 is coprime with the project count, so hot words reach every project
					text += " " + hotWords[i/97%len(hotWords)] + " tests"
				}
				m := &model.Memory{Type: model.Note, ScopeKind: model.ProjectScope, Scope: recallProject(i),
					Key: fmt.Sprintf("m-%d", i), Body: map[string]any{}, Text: text, Confidence: 0.8,
					Status: model.Active, FirstSeen: now, LastSeen: now}
				if _, err := tx.UpsertMemory(m, nil, 0); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		b.Fatal(err)
	}
	return st
}

func buildIndex(b *testing.B, st *store.Store) *index.Index {
	b.Helper()
	ms, err := st.ListMemories(store.ListFilter{Statuses: []model.Status{model.Active}})
	if err != nil {
		b.Fatal(err)
	}
	ix := index.New(index.MemoryFields, 0)
	for _, m := range ms {
		if err := ix.Upsert(index.MemoryDoc(m)); err != nil {
			b.Fatal(err)
		}
	}
	return ix
}

func TestStrictGateOnlyWhenRequested(t *testing.T) {
	target := time.Millisecond
	if targetFails(2*time.Millisecond, target, false) {
		t.Fatal("a latency miss failed a run that is not strict")
	}
	if !targetFails(2*time.Millisecond, target, true) {
		t.Fatal("a latency miss passed a strict run")
	}
}

// targetFails reports whether a p99 that misses target fails the run. Only a
// strict run fails on latency: shared runners are too noisy for it otherwise.
func targetFails(p99, target time.Duration, strict bool) bool {
	return strict && p99 >= target
}

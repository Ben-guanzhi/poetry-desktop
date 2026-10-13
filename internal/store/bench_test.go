package store

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"poetry-desktop/internal/model"
)

// The corpus these benchmarks sit on is one real dataset's worth of rows:
// 全唐诗 is 58k poems, so 50k with ~6-line content is the scale a long-lived
// install actually searches and rebuilds. Everything below is Θ(that table),
// not Θ(the query).

const (
	benchCorpusRows  = 50000
	benchCorpusChunk = 1000 // same per-file batch size the downloader uses
)

var (
	benchCorpusOnce sync.Once
	benchCorpusDir  string
	benchCorpusErr  error
)

// benchCorpus builds one populated temp database once for the whole run, then
// opens a fresh Store on it per caller, so no benchmark pollutes another's
// counts.
func benchCorpus(tb testing.TB) *Store {
	tb.Helper()
	benchCorpusOnce.Do(func() {
		dir, err := os.MkdirTemp("", "poetry-bench-corpus")
		if err != nil {
			benchCorpusErr = err
			return
		}
		benchCorpusDir = dir
		s, err := New(dir)
		if err != nil {
			benchCorpusErr = err
			return
		}
		for start := 0; start < benchCorpusRows; start += benchCorpusChunk {
			tx, err := s.Begin()
			if err != nil {
				benchCorpusErr = err
				s.Close()
				return
			}
			poems := make([]model.Poem, 0, benchCorpusChunk)
			for i := start; i < start+benchCorpusChunk; i++ {
				poems = append(poems, model.Poem{
					ID:      fmt.Sprintf("全唐诗-poet.tang.%d.json-%d", i/benchCorpusChunk, i%benchCorpusChunk),
					Dynasty: model.DynastyTang,
					Author:  fmt.Sprintf("作者%05d", i%3000),
					Title:   fmt.Sprintf("篇名%05d", i),
					Content: "君不见黄河之水天上来奔流到海不复回君不见高堂明镜悲白发朝如青丝暮成雪" + fmt.Sprintf("尾%05d", i),
					Tags:    "写景,哲理",
					Chapter: fmt.Sprintf("卷%03d", i%300),
					Type:    "poem",
				})
			}
			if _, err := s.ImportPoems(tx, poems); err != nil {
				benchCorpusErr = err
				tx.Rollback()
				s.Close()
				return
			}
			if err := tx.Commit(); err != nil {
				benchCorpusErr = err
				s.Close()
				return
			}
		}
		benchCorpusErr = s.Close()
	})
	if benchCorpusErr != nil {
		tb.Fatalf("build bench corpus: %v", benchCorpusErr)
	}
	s, err := New(benchCorpusDir)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	return s
}

// C1 baseline: every DownloadAndImport run ends with RebuildFTS, so this is the
// per-dataset import cost the downloader pays even when a re-run inserts zero
// rows. The triggers already index every insert, so the rebuild is a full
// Θ(table) resync for a Θ(1) "nothing changed" answer.
func BenchmarkRebuildFTS(b *testing.B) {
	s := benchCorpus(b)
	for i := 0; i < b.N; i++ {
		if err := s.RebuildFTS(); err != nil {
			b.Fatal(err)
		}
	}
}

// C1 baseline, the whole import-side path a re-run takes: re-insert the same
// 1000 rows (INSERT OR IGNORE answers 0 every time) and rebuild the index.
// This is what one unchanged dataset costs per settings-button press.
func BenchmarkReimportChunkPlusRebuild(b *testing.B) {
	s := benchCorpus(b)
	poems := reimportChunkFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := s.Begin()
		if err != nil {
			b.Fatal(err)
		}
		if _, err := s.ImportPoems(tx, poems); err != nil {
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		if err := s.RebuildFTS(); err != nil {
			b.Fatal(err)
		}
	}
}

// C1 after: the same re-run with the rebuild gated off (zero rows inserted).
// Run it in the same process as BenchmarkReimportChunkPlusRebuild; the pair
// is the in-process before/after.
func BenchmarkReimportChunkGated(b *testing.B) {
	s := benchCorpus(b)
	poems := reimportChunkFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := s.Begin()
		if err != nil {
			b.Fatal(err)
		}
		inserted, err := s.ImportPoems(tx, poems)
		if err != nil {
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		if inserted != 0 {
			b.Fatalf("re-run inserted %d rows, want 0 (the gate key must see a pure re-import)", inserted)
		}
	}
}

func reimportChunkFixture() []model.Poem {
	poems := make([]model.Poem, 0, benchCorpusChunk)
	for i := 0; i < benchCorpusChunk; i++ {
		poems = append(poems, model.Poem{
			ID:      fmt.Sprintf("全唐诗-poet.tang.0.json-%d", i),
			Dynasty: model.DynastyTang,
			Author:  "李白",
			Title:   fmt.Sprintf("篇名%05d", i),
			Content: "君不见黄河之水天上来奔流到海不复回",
			Type:    "poem",
		})
	}
	return poems
}

// C3 baseline: a two-character term cannot be expressed as a trigram, so
// SearchPoems falls back to the four-column LIKE scan over the whole corpus.
func BenchmarkSearchShortQueryLike(b *testing.B) {
	s := benchCorpus(b)
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchPoems("李白", 200, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// C3 counterpoint: the same intent through the FTS index (3+ chars) is an
// index seek, to show the fallback is the outlier, not search in general.
func BenchmarkSearchFTSQuery(b *testing.B) {
	s := benchCorpus(b)
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchPoems("黄河之水", 200, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// C4 baseline: loadSettings opens with PoemCount("") plus one
// PoemCountByDataset per dataset — 19 round trips, each answering "how many
// rows carry this id prefix". Here 18 dataset dirs over the corpus.
func BenchmarkSettingsPerDatasetCounts(b *testing.B) {
	s := benchCorpus(b)
	dirs := make([]string, 18)
	for i := range dirs {
		dirs[i] = fmt.Sprintf("全唐诗%d", i)
	}
	dirs[0] = "全唐诗"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.PoemCount(""); err != nil {
			b.Fatal(err)
		}
		for _, d := range dirs {
			if _, err := s.PoemCountByDataset(d); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// C4 counterpoint: the shipped grouped pass, Store.PoemCountsAllDatasets.
func BenchmarkSettingsGroupedCount(b *testing.B) {
	s := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counts, err := s.PoemCountsAllDatasets()
		if err != nil {
			b.Fatal(err)
		}
		if counts["全唐诗"] != benchCorpusRows {
			b.Fatalf("grouped counts = %v, want the whole corpus under 全唐诗", counts)
		}
	}
}

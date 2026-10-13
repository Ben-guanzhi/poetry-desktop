package fetcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"poetry-desktop/internal/model"
	"poetry-desktop/internal/store"
)

// The re-run baseline: "下载全部数据集" or a single dataset button on an
// already-imported corpus. Every row answers INSERT OR IGNORE = 0 inserted,
// yet the run still ends with a full RebuildFTS over the whole table. This
// measures what one unchanged dataset costs end to end.

const (
	benchFiles = 20
	benchRows  = 1000 // poems per file, like the real poet.*.json payloads
)

var (
	benchOnce    sync.Once
	benchDir     string
	benchData    string
	benchErr     error
	benchDataset = model.Dataset{
		Name: "bench", Dir: "bench", Dynasty: model.DynastyTang, Type: "poem", AutoDir: true,
	}
)

func benchSetup(tb testing.TB) (string, string) {
	tb.Helper()
	benchOnce.Do(func() {
		root, err := os.MkdirTemp("", "fetcher-bench")
		if err != nil {
			benchErr = err
			return
		}
		benchDir = root
		dataDir := filepath.Join(root, "data")
		benchData = filepath.Join(dataDir, "bench")
		if err := os.MkdirAll(benchData, 0755); err != nil {
			benchErr = err
			return
		}
		for f := 0; f < benchFiles; f++ {
			type raw struct {
				Author     string   `json:"author"`
				Paragraphs []string `json:"paragraphs"`
				Title      string   `json:"title"`
				ID         string   `json:"id"`
			}
			poems := make([]raw, benchRows)
			for i := range poems {
				poems[i] = raw{
					Author:     fmt.Sprintf("作者%03d", i%800),
					Paragraphs: []string{"床前明月光疑是地上霜", "举头望明月低头思故乡"},
					Title:      fmt.Sprintf("篇%04d", i),
					ID:         fmt.Sprintf("bench-f%02d-%d", f, i),
				}
			}
			b, err := json.Marshal(poems)
			if err != nil {
				benchErr = err
				return
			}
			if err := os.WriteFile(filepath.Join(benchData, fmt.Sprintf("bench.%d.json", f)), b, 0644); err != nil {
				benchErr = err
				return
			}
		}
		// First import fills the corpus; the benchmarked runs add nothing.
		s, err := store.New(dataDir)
		if err != nil {
			benchErr = err
			return
		}
		d := New(dataDir, s, nil)
		benchErr = d.DownloadAndImport(benchDataset)
		s.Close()
	})
	if benchErr != nil {
		tb.Fatalf("bench setup: %v", benchErr)
	}
	return benchDir, filepath.Join(benchDir, "data")
}

func BenchmarkReimportNoNewRows(b *testing.B) {
	_, dataDir := benchSetup(b)
	s, err := store.New(dataDir)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	d := New(dataDir, s, nil)
	ds := benchDataset

	// Warm: one run before the timer to settle page caches and WAL state.
	if err := d.DownloadAndImport(ds); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := d.DownloadAndImport(ds); err != nil {
			b.Fatal(err)
		}
	}
}

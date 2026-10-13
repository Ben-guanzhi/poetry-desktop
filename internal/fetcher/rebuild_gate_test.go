package fetcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"poetry-desktop/internal/model"
	"poetry-desktop/internal/store"
)

// The rebuild gate: DownloadAndImport ends with a full RebuildFTS, which is
// Θ(entire poems table). The AFTER INSERT triggers already index every row an
// import adds, so a re-run that inserts nothing pays a corpus sweep for
// nothing, and a run that inserts rows still gets the repair pass. These tests
// count rebuild calls through the injected d.rebuild to pin both halves.

type gateFixture struct {
	dataDir string
	d       *Downloader
	s       *store.Store
	rebuild int
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	s, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d := New(dataDir, s, nil)
	f := &gateFixture{dataDir: dataDir, d: d, s: s}
	d.rebuild = func() error {
		f.rebuild++
		return s.RebuildFTS()
	}
	return f
}

// writeLocalPoems puts poem payloads into an AutoDir dataset directory,
// numbered from `from`, so a second call models "one more file downloaded".
func (f *gateFixture) writeLocalPoems(t *testing.T, from, count int) {
	t.Helper()
	dir := filepath.Join(f.dataDir, "gate")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for fi := from; fi < from+count; fi++ {
		body := "["
		for i := 0; i < 5; i++ {
			if i > 0 {
				body += ","
			}
			body += fmt.Sprintf(`{"id":"gate-f%d-%d","title":" gate 篇%d","author":"李白","paragraphs":["床前明月光疑是地上霜"]}`, fi, i, fi*10+i)
		}
		body += "]"
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("gate.%d.json", fi)), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func gateDataset() model.Dataset {
	return model.Dataset{Name: "gate", Dir: "gate", Dynasty: model.DynastyTang, Type: "poem", AutoDir: true}
}

// Must-fire, then must-skip: the first import inserts rows and rebuilds; an
// identical re-run inserts 0 rows and must not touch the index again.
// Deleting the gate (always rebuild) fails the second phase; deleting the
// rebuild entirely fails the first.
func TestRebuildFTSRunsOnlyWhenRowsWereInserted(t *testing.T) {
	f := newGateFixture(t)
	f.writeLocalPoems(t, 0, 2)
	ds := gateDataset()

	if err := f.d.DownloadAndImport(ds); err != nil {
		t.Fatal(err)
	}
	if f.rebuild != 1 {
		t.Fatalf("after a first import the rebuild must fire: calls = %d, want 1", f.rebuild)
	}

	// Re-run over the same files: every row answers INSERT OR IGNORE = 0.
	if err := f.d.DownloadAndImport(ds); err != nil {
		t.Fatal(err)
	}
	if f.rebuild != 1 {
		t.Fatalf("after a no-new-rows re-run the rebuild must be skipped: calls = %d, want 1", f.rebuild)
	}

	// The skip must be safe: the trigger-maintained index still answers an
	// FTS search for the imported text.
	poems, err := f.s.SearchPoems("床前明月光", 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 10 {
		t.Fatalf("search after the skipped rebuild = %d rows, want 10", len(poems))
	}
}

// A partial re-run (one new file added to an imported dataset) still fired
// the repair pass: the gate keys on rows inserted, not on files seen.
func TestRebuildFTSFiresOnPartialReimport(t *testing.T) {
	f := newGateFixture(t)
	f.writeLocalPoems(t, 0, 1)
	ds := gateDataset()
	if err := f.d.DownloadAndImport(ds); err != nil {
		t.Fatal(err)
	}
	if f.rebuild != 1 {
		t.Fatalf("first import: rebuild calls = %d, want 1", f.rebuild)
	}

	f.writeLocalPoems(t, 1, 1)
	if err := f.d.DownloadAndImport(ds); err != nil {
		t.Fatal(err)
	}
	if f.rebuild != 2 {
		t.Fatalf("import with new rows: rebuild calls = %d, want 2", f.rebuild)
	}
	n, err := f.s.PoemCount("")
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("corpus = %d rows, want 10", n)
	}
}

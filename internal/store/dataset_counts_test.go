package store

import (
	"fmt"
	"testing"

	"poetry-desktop/internal/model"
)

// PoemCountsAllDatasets replaces the per-dataset PoemCountByDataset loop in
// the settings page. That is only legitimate if the one grouped pass answers
// exactly what 18 prefix LIKEs answered: same count for every dir, zero for
// dirs with no rows, and no interference from dirs that share a prefix or
// from corpus ids without a dash.
func TestPoemCountsAllDatasetsMatchesPerDatasetCounts(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	tx, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var poems []model.Poem
	for i, ds := range model.Datasets {
		// Three datasets share 五代诗词; row counts differ per dir.
		n := (i % 3) + 1
		for j := 0; j < n; j++ {
			poems = append(poems, model.Poem{
				ID:      fmt.Sprintf("%s-file%d-%d.json", ds.Dir, i, j),
				Dynasty: ds.Dynasty,
				Title:   fmt.Sprintf("篇%d-%d", i, j),
				Content: "床前明月光",
				Type:    ds.Type,
			})
		}
	}
	// A corpus id without a dash cannot belong to any dataset (dataset dirs
	// are looked up as "<dir>-"); it must not be charged to a real dir.
	poems = append(poems, model.Poem{ID: "nodash", Title: "无划线", Content: "x"})
	if _, err := s.ImportPoems(tx, poems); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	counts, err := s.PoemCountsAllDatasets()
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, ds := range model.Datasets {
		if seen[ds.Dir] {
			continue
		}
		seen[ds.Dir] = true
		want, err := s.PoemCountByDataset(ds.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if counts[ds.Dir] != want {
			t.Errorf("grouped count for %q = %d, per-dataset LIKE count = %d", ds.Dir, counts[ds.Dir], want)
		}
	}

	// A dir that is a strict prefix of another must not absorb its rows:
	// LIKE '五代-%' must not count '五代诗词-...' ids even though they share
	// leading characters.
	const missing = "五代"
	if want, _ := s.PoemCountByDataset(missing); want != 0 || counts[missing] != want {
		t.Fatalf("prefix dir %q: grouped = %d, LIKE = %d, want both 0", missing, counts[missing], want)
	}

	// The dashless id is counted nowhere a caller asks about: the total is
	// still every row, but no dataset lookup claims it.
	total, err := s.PoemCount("")
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, n := range counts {
		sum += n
	}
	if sum != total {
		t.Fatalf("grouped buckets sum to %d, table holds %d", sum, total)
	}
}

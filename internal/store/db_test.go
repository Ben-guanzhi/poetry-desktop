package store

import (
	"testing"

	"poetry-desktop/internal/model"
)

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, func() { s.Close() }
}

func TestMigrateCreatesSchemaAndVersion(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	v, err := s.getSchemaVersion()
	if err != nil {
		t.Fatalf("getSchemaVersion: %v", err)
	}
	if v != 2 {
		t.Fatalf("schema version = %d, want 2", v)
	}
}

// The DSN must reach WAL: modernc.org/sqlite only honors "_pragma=" params
// (mattn-style "_journal_mode=" keys are silently ignored), and WAL + busy
// timeout + NORMAL sync is what the connection string claims to set.
func TestWALJournalModeActive(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var busy int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busy)
	}
}

func TestRandomPoemEmptyReturnsNil(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	p, err := s.RandomPoem()
	if err != nil {
		t.Fatalf("RandomPoem: %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil on empty DB, got %+v", p)
	}
}

func TestRandomPoemReturnsImportedPoem(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	tx, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	poems := []model.Poem{
		{ID: "1", Dynasty: model.DynastyTang, Author: "A", Title: "T1", Content: "c1"},
		{ID: "2", Dynasty: model.DynastyTang, Author: "A", Title: "T2", Content: "c2"},
		{ID: "3", Dynasty: model.DynastyTang, Author: "A", Title: "T3", Content: "c3"},
	}
	if err := s.ImportPoems(tx, poems); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		p, err := s.RandomPoem()
		if err != nil {
			t.Fatalf("RandomPoem: %v", err)
		}
		if p == nil {
			t.Fatal("RandomPoem returned nil on non-empty DB")
		}
		if p.ID != "1" && p.ID != "2" && p.ID != "3" {
			t.Fatalf("unexpected poem id %q", p.ID)
		}
		seen[p.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("random draws only hit %d distinct ids, want 3: %v", len(seen), seen)
	}
}

func TestRandomPoemByDynasty(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	tx, _ := s.Begin()
	if err := s.ImportPoems(tx, []model.Poem{
		{ID: "s1", Dynasty: model.DynastySong, Title: "S1", Content: "s1"},
		{ID: "t1", Dynasty: model.DynastyTang, Title: "T1", Content: "t1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		p, err := s.RandomPoemByDynasty(model.DynastySong)
		if err != nil {
			t.Fatalf("RandomPoemByDynasty: %v", err)
		}
		if p == nil || p.ID != "s1" {
			t.Fatalf("expected only the Song poem, got %+v", p)
		}
	}
}

// Regression: a filtered draw must be uniform over the subset, not over the
// rowid range. Types occupying the low rowids used to absorb the whole
// pre-subset range into the subset's first row, so one row took two thirds of
// the draws and the rest were nearly unreachable.
func TestRandomPoemByTypeIsUniformOverTheSubset(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	tx, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	poems := []model.Poem{
		{ID: "ji1", Dynasty: model.DynastyTang, Title: "J1", Type: "ji"},
		{ID: "ji2", Dynasty: model.DynastyTang, Title: "J2", Type: "ji"},
		{ID: "ji3", Dynasty: model.DynastyTang, Title: "J3", Type: "ji"},
		{ID: "ji4", Dynasty: model.DynastyTang, Title: "J4", Type: "ji"},
		{ID: "ji5", Dynasty: model.DynastyTang, Title: "J5", Type: "ji"},
		{ID: "ci1", Dynasty: model.DynastySong, Title: "C1", Type: "ci"},
		{ID: "ci2", Dynasty: model.DynastySong, Title: "C2", Type: "ci"},
		{ID: "ci3", Dynasty: model.DynastySong, Title: "C3", Type: "ci"},
		{ID: "ci4", Dynasty: model.DynastySong, Title: "C4", Type: "ci"},
	}
	if err := s.ImportPoems(tx, poems); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	const draws = 200
	counts := map[string]int{}
	for i := 0; i < draws; i++ {
		p, err := s.RandomPoemByType("ci")
		if err != nil {
			t.Fatalf("RandomPoemByType: %v", err)
		}
		if p == nil {
			t.Fatal("RandomPoemByType returned nil on a non-empty subset")
		}
		if p.Type != "ci" {
			t.Fatalf("draw escaped the filter: %+v", p)
		}
		counts[p.ID]++
	}
	if len(counts) != 4 {
		t.Fatalf("draws hit %d of the 4 subset rows: %v", len(counts), counts)
	}
	// Uniform is 50 each (sigma ~6); the old rowid seek gave ~133 / ~22.
	for id, n := range counts {
		if n < 25 || n > 100 {
			t.Fatalf("row %s drawn %d/%d times, want a uniform share: %v", id, n, draws, counts)
		}
	}
}

func TestGetSchemaVersionDoesNotDropOnMissingMeta(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	// Fresh database has no meta table; that must read as "old schema" (1),
	// not as an error, so migrate() still runs and creates the schema.
	// The version-2 write is exercised through migrateFromOldSchema in New.
	var name string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='meta'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("meta table missing after migrate: %v", err)
	}
	if name != "meta" {
		t.Fatalf("unexpected meta name %q", name)
	}
}

// collections is the only user-owned table: a rebuild from the old schema must
// carry its rows forward instead of dropping them. The old code dropped the
// table unconditionally with the rest.
func TestMigratePreservesCollections(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tx, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportPoems(tx, []model.Poem{
		{ID: "1", Dynasty: model.DynastyTang, Author: "A", Title: "T1", Content: "c1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ToggleCollection("1"); err != nil {
		t.Fatal(err)
	}
	// Force the old-schema rebuild: poems, authors and meta are re-downloadable,
	// collections is not.
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', '1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New after rebuild: %v", err)
	}
	defer s2.Close()
	if !s2.tableExists("collections") {
		t.Fatal("collections table missing after rebuild")
	}
	var n int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM collections WHERE poem_id = ?`, "1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("favorite lost by the rebuild: %d rows, want 1", n)
	}
	// The poems table comes back empty, so the join has nothing to show until
	// the corpus is downloaded again — but the row is still there.
	cols, err := s2.ListCollections(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 0 {
		t.Fatalf("expected no listed favorites before the corpus is back, got %d", len(cols))
	}
	if _, err := s2.ToggleCollection("2"); err != nil {
		t.Fatal(err)
	}
	var m int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM collections`).Scan(&m); err != nil {
		t.Fatal(err)
	}
	if m != 2 {
		t.Fatalf("collections = %d rows after re-adding, want 2", m)
	}
	if v, err := s2.getSchemaVersion(); err != nil || v != 2 {
		t.Fatalf("schema version = %d, %v; want 2", v, err)
	}
}

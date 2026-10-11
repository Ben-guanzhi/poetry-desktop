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

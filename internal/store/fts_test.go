package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"poetry-desktop/internal/model"
)

func importPoems(t *testing.T, s *Store, poems []model.Poem) {
	t.Helper()
	tx, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportPoems(tx, poems); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func ftTestPoems() []model.Poem {
	return []model.Poem{
		{ID: "p1", Dynasty: model.DynastyTang, Author: "李白", Title: "将进酒", Content: "君不见黄河之水天上来，奔流到海不复回", Chapter: "卷一"},
		{ID: "p2", Dynasty: model.DynastyTang, Author: "杜甫", Title: "春望", Content: "国破山河在，城春草木深", Chapter: "卷一"},
		{ID: "p3", Dynasty: model.DynastySong, Author: "苏轼", Title: "水调歌头", Content: "明月几时有，把酒问青天", Chapter: "卷二"},
	}
}

// The whole point of the FTS wiring: a Chinese query has to actually find
// anything. Before tokenize='trigram' the default unicode61 tokenizer dropped
// every CJK code point, so the index held the text and returned zero rows for
// it anyway.
func TestSearchPoemsFTSMatchesChineseText(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	poems, err := s.SearchPoems(`黄河之水天上来`, 200, 0)
	if err != nil {
		t.Fatalf("SearchPoems: %v", err)
	}
	if len(poems) != 1 || poems[0].ID != "p1" {
		t.Fatalf("search = %d rows %v, want p1 only", len(poems), poems)
	}
}

// A term shorter than trigramMinChars cannot be expressed as a trigram and the
// index reports that as zero rows, not an error. Without the LIKE fallback a
// two-character search would come back empty while the text is in the table.
func TestSearchPoemsShortQueryFallsBackToLike(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	for _, q := range []string{`李`, `李白`, `卷一`} {
		poems, err := s.SearchPoems(q, 200, 0)
		if err != nil {
			t.Fatalf("SearchPoems(%q): %v", q, err)
		}
		if len(poems) == 0 {
			t.Fatalf("SearchPoems(%q) returned nothing; the fallback must serve short terms", q)
		}
	}
}

// The index must not be load-bearing: if it is gone, search still answers.
func TestSearchPoemsFallsBackWhenFTSIsMissing(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	if _, err := s.db.Exec(`DROP TABLE poems_fts`); err != nil {
		t.Fatal(err)
	}
	poems, err := s.SearchPoems(`明月几时有`, 200, 0)
	if err != nil {
		t.Fatalf("SearchPoems without the index: %v", err)
	}
	if len(poems) != 1 || poems[0].ID != "p3" {
		t.Fatalf("search = %v, want p3 only", poems)
	}
}

// A placeholder table squatting on the index name (for instance after an
// interrupted migration) makes MATCH fail at parse time. That must fall back
// to LIKE instead of surfacing a SQL syntax error to the UI.
func TestSearchPoemsFallsBackWhenFTSIsBroken(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	if _, err := s.db.Exec(`DROP TABLE poems_fts`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE poems_fts (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	poems, err := s.SearchPoems(`城春草木深`, 200, 0)
	if err != nil {
		t.Fatalf("SearchPoems over a broken index: %v", err)
	}
	if len(poems) != 1 || poems[0].ID != "p2" {
		t.Fatalf("search = %v, want p2 only", poems)
	}
}

// chapter was never indexed by the v2 table even though SearchPoems searches
// it. It must be findable through the index now, not only through LIKE.
func TestFTSIndexesChapter(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	poems, err := s.SearchPoems(`卷二`, 200, 0)
	if err != nil {
		t.Fatalf("SearchPoems: %v", err)
	}
	if len(poems) != 1 || poems[0].ID != "p3" {
		t.Fatalf("chapter search = %v, want p3 only", poems)
	}
}

// External-content FTS5 cannot maintain itself, so the triggers are the only
// thing standing between the index and drift. Import must not need
// RebuildFTS at all.
func TestFTSTriggersKeepTheIndexInSync(t *testing.T) {
	s, close := newTestStore(t)
	defer close()

	// Insert: indexed by the trigger alone, with no RebuildFTS call.
	importPoems(t, s, ftTestPoems())
	poems, err := s.SearchPoems(`国破山河在`, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 1 || poems[0].ID != "p2" {
		t.Fatalf("after insert, search = %v, want p2 only", poems)
	}

	// Update: the old text must stop matching and the new one must start.
	if _, err := s.db.Exec(`UPDATE poems SET content = '床前明月光，疑是地上霜' WHERE id = 'p2'`); err != nil {
		t.Fatal(err)
	}
	poems, err = s.SearchPoems(`国破山河在`, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 0 {
		t.Fatalf("after update, stale text still matches: %v", poems)
	}
	poems, err = s.SearchPoems(`床前明月光`, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 1 || poems[0].ID != "p2" {
		t.Fatalf("after update, new text = %v, want p2 only", poems)
	}

	// Delete: the entry must go with the row.
	if _, err := s.db.Exec(`DELETE FROM poems WHERE id = 'p2'`); err != nil {
		t.Fatal(err)
	}
	poems, err = s.SearchPoems(`床前明月光`, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 0 {
		t.Fatalf("after delete, %v still matches", poems)
	}
}

// ImportPoems uses INSERT OR IGNORE, so a re-import of a dataset is ignored
// row by row. The ignored rows must neither duplicate the index nor break it:
// they were already indexed when they were first inserted.
func TestFTSIndexSurvivesAReimport(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	importPoems(t, s, ftTestPoems())

	var indexed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM poems_fts`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 3 {
		t.Fatalf("index holds %d rows after a re-import, want 3", indexed)
	}
	poems, err := s.SearchPoems(`城春草木深`, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 1 || poems[0].ID != "p2" {
		t.Fatalf("after a re-import, search = %v, want p2 only", poems)
	}
}

// The importers call RebuildFTS after every import, and the triggers have
// already indexed those rows by then. So 'rebuild' must be a full resync rather
// than an append: it has to tolerate rowids the index already holds, and drop
// ones that are not in poems at all. The old NOT IN version failed outright on
// the first case, and the new one replaces the session-end sweep that was the
// only maintenance the index ever had.
func TestRebuildFTSIsAFullResync(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	// Poison the index directly, bypassing the trigger.
	if _, err := s.db.Exec(
		`INSERT INTO poems_fts(rowid, content, author, title, chapter)
		 VALUES (999, '完全无关的污染文本xyz', '污染作者', '污染标题', '污染章节')`,
	); err != nil {
		t.Fatal(err)
	}
	countMatch := func(pattern string) int {
		var n int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM poems_fts WHERE poems_fts MATCH ?`, pattern,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := countMatch(`"完全无关的污染文本xyz"`); got != 1 {
		t.Fatalf("before the rebuild, poison rows = %d, want 1", got)
	}

	// Rebuilding over rows the trigger already indexed must not error, and it
	// must clear the phantom rowid.
	if err := s.RebuildFTS(); err != nil {
		t.Fatalf("RebuildFTS over trigger-maintained rows: %v", err)
	}
	if got := countMatch(`"完全无关的污染文本xyz"`); got != 0 {
		t.Fatalf("phantom row survived the rebuild: %d", got)
	}
	poems, err := s.SearchPoems(`完全无关的污染文本`, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 0 {
		t.Fatalf("phantom row still searchable after the rebuild: %v", poems)
	}
	poems, err = s.SearchPoems(`城春草木深`, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(poems) != 1 || poems[0].ID != "p2" {
		t.Fatalf("after the rebuild, search = %v, want p2 only", poems)
	}
}

// ClearAllPoems used to wipe the index before poems, which would have made the
// AFTER DELETE trigger fail on entries that were already gone.
func TestClearAllPoemsSweepsTheIndex(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	if err := s.ClearAllPoems(); err != nil {
		t.Fatalf("ClearAllPoems: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM poems`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("poems = %d, want 0", n)
	}
	var indexed int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM poems_fts`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 0 {
		t.Fatalf("index = %d rows, want 0", indexed)
	}
}

func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{in: "", want: "", wantOK: false},
		{in: "   ", want: "", wantOK: false},
		{in: `李`, want: "", wantOK: false},
		{in: `李白`, want: "", wantOK: false},
		{in: `卷一`, want: "", wantOK: false},
		{in: `黄河水`, want: `"黄河水"`, wantOK: true},
		{in: `明月几时有`, want: `"明月几时有"`, wantOK: true},
		// A short term anywhere in the query forces the LIKE path.
		{in: `李白 将进酒`, want: "", wantOK: false},
		// Punctuation and quotes are kept verbatim inside the phrase, so they
		// cannot be parsed as FTS5 operators.
		{in: `a"b c`, want: "", wantOK: false},
		{in: `引号""abc`, want: `"引号""""abc"`, wantOK: true},
		{in: `明月 AND 青天`, want: "", wantOK: false},
		{in: `明月月 青天天`, want: `"明月月" AND "青天天"`, wantOK: true},
		{in: `abc def`, want: `"abc" AND "def"`, wantOK: true},
	}
	for _, c := range cases {
		got, ok := buildFTSQuery(c.in)
		if ok != c.wantOK {
			t.Fatalf("buildFTSQuery(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
		if got != c.want {
			t.Fatalf("buildFTSQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The FTS query must never be rejected because of what the user typed. Every
// shape buildFTSQuery accepts has to parse against a real index.
func TestFTSQueryIsAlwaysParseable(t *testing.T) {
	s, close := newTestStore(t)
	defer close()
	importPoems(t, s, ftTestPoems())

	for _, in := range []string{`明月几时有`, `引号""abc`, `明月月和青天天`, `明月月 青天天`, `abc def`, `，。！？一二三`} {
		q, ok := buildFTSQuery(in)
		if !ok {
			t.Skipf("%q does not use the index", in)
			continue
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM poems_fts WHERE poems_fts MATCH ?`, q).Scan(&n); err != nil {
			t.Fatalf("MATCH %q (from %q) rejected: %v", q, in, err)
		}
	}
}

// A database sitting on the v2 shape has the old unicode61 index. Opening it
// must rebuild the index and leave a working Chinese search, without dropping
// poems, and must drop the authors table on the way to the latest version.
func TestMigrateV2RebuildsTheIndexAndDropsAuthors(t *testing.T) {
	dir := t.TempDir()
	if err := buildV2Database(filepath.Join(dir, "poetry.db")); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	v, err := s.getSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("schema version = %d, want %d", v, schemaVersion)
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM poems`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("poems = %d after migration, want 3", n)
	}
	if s.tableExists("authors") {
		t.Fatal("authors table survived migration to v4")
	}

	poems, err := s.SearchPoems(`黄河之水天上来`, 200, 0)
	if err != nil {
		t.Fatalf("SearchPoems on the migrated database: %v", err)
	}
	if len(poems) != 1 || poems[0].ID != "p1" {
		t.Fatalf("migrated search = %v, want p1 only", poems)
	}
}

// buildV2Database lays down exactly what the pre-v3 schema looked like, so the
// migration test exercises the real upgrade path rather than a fresh build.
func buildV2Database(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE poems (
			id TEXT PRIMARY KEY, dynasty TEXT, author TEXT, title TEXT, rhythmic TEXT,
			content TEXT, tags TEXT, chapter TEXT, section TEXT, poem_type TEXT
		)`,
		`CREATE TABLE authors (
			id TEXT PRIMARY KEY, name TEXT, dynasty TEXT, description TEXT
		)`,
		`CREATE TABLE collections (poem_id TEXT PRIMARY KEY, created_at INTEGER DEFAULT (unixepoch()))`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		// The v2 definition: default unicode61 tokenizer, no chapter column.
		`CREATE VIRTUAL TABLE poems_fts USING fts5(
			content, author, title, content_rowid=rowid, content=poems
		)`,
		`INSERT INTO meta (key, value) VALUES ('schema_version', '2')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	for _, p := range ftTestPoems() {
		if _, err := db.Exec(
			`INSERT INTO poems (id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Dynasty, p.Author, p.Title, p.Rhythmic, p.Content, p.Tags, p.Chapter, p.Section, p.Type,
		); err != nil {
			return err
		}
	}
	return nil
}

package store

import (
	"database/sql"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"poetry-desktop/internal/model"

	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	dataDir string
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	// modernc.org/sqlite reads PRAGMAs via "_pragma=" query params (the
	// mattn-style "_journal_mode=WAL&_busy_timeout=" keys are ignored by
	// modernc, so WAL would silently stay off). Pragma values are taken
	// verbatim and run as "PRAGMA <value>", so parens need no escaping.
	dbPath := filepath.Join(dataDir, "poetry.db")
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, dataDir: dataDir}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// schemaVersion is what a freshly migrated database reports in meta.
// 2 introduced the poems/authors/meta tables; 3 rebuilt poems_fts (see
// migrateFTSToTrigram) because the v2 index could not match Chinese text at all;
// 4 dropped the authors table, whose only two readers were gone and whose rows
// were pure corpus data (every poem already carries its own author column).
const schemaVersion = 4

func (s *Store) migrate() error {
	version, err := s.getSchemaVersion()
	if err != nil {
		return err
	}

	if version < 2 {
		// Old schema - need to migrate
		return s.migrateFromOldSchema()
	}
	if version < 3 {
		if err := s.migrateFTSToTrigram(); err != nil {
			return err
		}
	}
	if version < schemaVersion {
		return s.migrateDropAuthors()
	}
	return nil
}

// migrateFTSToTrigram rebuilds the search index for v3 without touching the
// poems table: the index is derived data, so dropping it costs time only.
func (s *Store) migrateFTSToTrigram() error {
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS poems_fts`); err != nil {
		return err
	}
	if err := s.createFTS(); err != nil {
		return err
	}
	if err := s.RebuildFTS(); err != nil {
		return err
	}
	return s.setSchemaVersion(3)
}

// migrateDropAuthors removes the authors table. It was imported from the corpus
// but never read: its only two readers, ListAuthors and AuthorCount, were
// deleted, and every poem already carries its own author column, so the table
// held write-only rows. collections is the only user data and is untouched.
func (s *Store) migrateDropAuthors() error {
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS authors`); err != nil {
		return err
	}
	return s.setSchemaVersion(schemaVersion)
}

func (s *Store) setSchemaVersion(v int) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)`, v)
	return err
}

func (s *Store) getSchemaVersion() (int, error) {
	// Fresh database: "meta" table does not exist yet. Only that specific
	// condition means "old schema" (which is also the brand-new state).
	// Any other error is a real fault and must abort migration instead of
	// dropping tables under a transient read failure.
	var name string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='meta'`,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	var v int
	err = s.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 1, nil // meta table present but version row missing
	}
	if err != nil {
		return 0, err
	}
	return v, nil
}

func (s *Store) tableExists(name string) bool {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&n)
	return err == nil && n > 0
}

func (s *Store) migrateFromOldSchema() error {
	// Read favorites first: collections is user data, everything else is
	// re-downloadable corpus, so a rebuild from the old schema must not wipe
	// the ones the user saved by hand. Their poems are rebuilt empty at this
	// point, so the rows read as empty until the corpus is downloaded again —
	// ListCollections joins back on poems.id and does not show them meanwhile.
	var favorites []string
	if s.tableExists("collections") {
		rows, err := s.db.Query(`SELECT poem_id FROM collections`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			favorites = append(favorites, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	// Drop and recreate all tables for a clean start
	dropStmts := []string{
		`DROP TABLE IF EXISTS poems_fts`,
		`DROP TABLE IF EXISTS poems`,
		`DROP TABLE IF EXISTS collections`,
		`DROP TABLE IF EXISTS meta`,
	}
	for _, stmt := range dropStmts {
		_, _ = s.db.Exec(stmt)
	}

	// Create new schema
	schema := `
CREATE TABLE poems (
	id TEXT PRIMARY KEY,
	dynasty TEXT,
	author TEXT,
	title TEXT,
	rhythmic TEXT,
	content TEXT,
	tags TEXT,
	chapter TEXT,
	section TEXT,
	poem_type TEXT
);

CREATE TABLE collections (
	poem_id TEXT PRIMARY KEY,
	created_at INTEGER DEFAULT (unixepoch())
);

CREATE TABLE meta (
	key TEXT PRIMARY KEY,
	value TEXT
);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}

	// Create indexes
	indexStmts := []string{
		`CREATE INDEX idx_poems_dynasty ON poems(dynasty)`,
		`CREATE INDEX idx_poems_author ON poems(author)`,
		`CREATE INDEX idx_poems_title ON poems(title)`,
		`CREATE INDEX idx_poems_chapter ON poems(chapter)`,
	}
	for _, stmt := range indexStmts {
		_, _ = s.db.Exec(stmt)
	}

	if err := s.createFTS(); err != nil {
		return err
	}

	// Play the favorites back into the rebuilt table (poem_id is the primary
	// key, so a duplicate cannot be introduced).
	if len(favorites) > 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare(`INSERT OR IGNORE INTO collections (poem_id) VALUES (?)`)
		if err != nil {
			tx.Rollback()
			return err
		}
		defer stmt.Close()
		for _, id := range favorites {
			if _, err := stmt.Exec(id); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	// Set version: this path already builds the v4 schema through createFTS,
	// so a fresh database lands directly on the latest version.
	return s.setSchemaVersion(schemaVersion)
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Begin() (*sql.Tx, error) {
	return s.db.Begin()
}

// ImportPoems inserts poems in one prepared batch and reports how many rows
// actually landed. INSERT OR IGNORE answers 0 for rows the corpus already has,
// so the count is how the callers tell a real import from a no-op re-run
// without scanning the table again.
func (s *Store) ImportPoems(tx *sql.Tx, poems []model.Poem) (int, error) {
	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO poems (id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, p := range poems {
		res, err := stmt.Exec(p.ID, string(p.Dynasty), p.Author, p.Title, p.Rhythmic, p.Content, p.Tags, p.Chapter, p.Section, p.Type)
		if err != nil {
			return inserted, err
		}
		// A driver that cannot answer RowsAffected reports -1/0 here; treating
		// "unknown" as "inserted" keeps the caller's rebuild gate conservative:
		// it rebuilds rather than silently skipping.
		n, err := res.RowsAffected()
		if err != nil || n != 0 {
			inserted++
		}
	}
	return inserted, nil
}

// createFTS builds the search index plus the triggers that keep it honest.
//
// tokenize='trigram' is mandatory, not cosmetic: the default unicode61
// tokenizer does not tokenize CJK at all in the SQLite build this project
// links (modernc.org/sqlite), so every Chinese query matched zero rows and
// the index was dead weight. Trigram matches substrings, which is the same
// semantics SearchPoems' LIKE '%q%' gives users, so the two search paths
// agree on what a query means.
func (s *Store) createFTS() error {
	_, err := s.db.Exec(`
		CREATE VIRTUAL TABLE poems_fts USING fts5(
			content,
			author,
			title,
			chapter,
			content_rowid=rowid,
			content=poems,
			tokenize='trigram'
		)
	`)
	if err != nil {
		return err
	}
	// External content means FTS5 stores only pointers into poems, so it
	// cannot maintain itself and the index would drift on every write. These
	// triggers replace the session-end O(N) sweep RebuildFTS used to be the
	// only maintenance.
	_, err = s.db.Exec(`
		CREATE TRIGGER poems_fts_ins AFTER INSERT ON poems BEGIN
			INSERT INTO poems_fts(rowid, content, author, title, chapter)
			VALUES (new.rowid, new.content, new.author, new.title, new.chapter);
		END
	`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		CREATE TRIGGER poems_fts_del AFTER DELETE ON poems BEGIN
			INSERT INTO poems_fts(poems_fts, rowid, content, author, title, chapter)
			VALUES ('delete', old.rowid, old.content, old.author, old.title, old.chapter);
		END
	`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		CREATE TRIGGER poems_fts_upd AFTER UPDATE OF content, author, title, chapter ON poems BEGIN
			INSERT INTO poems_fts(poems_fts, rowid, content, author, title, chapter)
			VALUES ('delete', old.rowid, old.content, old.author, old.title, old.chapter);
			INSERT INTO poems_fts(rowid, content, author, title, chapter)
			VALUES (new.rowid, new.content, new.author, new.title, new.chapter);
		END
	`)
	return err
}

// RebuildFTS resyncs the whole index from poems. It is a repair and a final
// check for the importers, not the normal maintenance path: triggers do that.
// The 'rebuild' command re-derives every entry from the content table, so it
// also heals drift and clears rowids that are not in poems; a hand-written
// INSERT of a rowid the index already holds is an error for an
// external-content table, which is what the previous NOT IN version did the
// moment the triggers had already indexed a row.
func (s *Store) RebuildFTS() error {
	_, err := s.db.Exec(`INSERT INTO poems_fts(poems_fts) VALUES ('rebuild')`)
	return err
}

func (s *Store) PoemCount(dynasty model.Dynasty) (int, error) {
	var query string
	var args []any
	if dynasty == "" {
		query = `SELECT COUNT(*) FROM poems`
	} else {
		query = `SELECT COUNT(*) FROM poems WHERE dynasty = ?`
		args = append(args, string(dynasty))
	}
	var n int
	err := s.db.QueryRow(query, args...).Scan(&n)
	return n, err
}

func (s *Store) ListAuthorsFromPoems(dynasty model.Dynasty, limit, offset int) ([]string, error) {
	var query string
	var args []any
	if dynasty == "" {
		query = `SELECT DISTINCT author FROM poems WHERE author != '' ORDER BY author LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	} else {
		query = `SELECT DISTINCT author FROM poems WHERE dynasty = ? AND author != '' ORDER BY author LIMIT ? OFFSET ?`
		args = append(args, string(dynasty), limit, offset)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		res = append(res, name)
	}
	return res, rows.Err()
}

func (s *Store) ListPoemsByAuthor(author string, limit, offset int) ([]model.Poem, error) {
	rows, err := s.db.Query(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE author = ? ORDER BY title LIMIT ? OFFSET ?
	`, author, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

func (s *Store) ListPoemsByDynasty(dynasty model.Dynasty, limit, offset int) ([]model.Poem, error) {
	rows, err := s.db.Query(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE dynasty = ? ORDER BY RANDOM() LIMIT ? OFFSET ?
	`, string(dynasty), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

// ListPoemsByDataset returns all poems whose id carries the dataset dir
// prefix (ids are generated as "<dir>-<file>-<n>"), keeping insertion
// order (rowid).
// Unlike ListPoemsByDynasty this does not mix datasets that share a dynasty.
func (s *Store) ListPoemsByDataset(dir string, limit, offset int) ([]model.Poem, error) {
	rows, err := s.db.Query(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE id LIKE ? ORDER BY rowid LIMIT ? OFFSET ?
	`, dir+"-%", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

// ListBooksFromDataset returns the distinct book names (chapter column) of a
// dataset, e.g. the books crawled into the 维基文库 dataset.
func (s *Store) ListBooksFromDataset(dir string, limit, offset int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT chapter FROM poems
		WHERE id LIKE ? AND chapter != ''
		ORDER BY chapter LIMIT ? OFFSET ?
	`, dir+"-%", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		res = append(res, name)
	}
	return res, rows.Err()
}

// ListPoemsByBookInDataset returns the poems (chapters) of one book within a
// dataset, keeping insertion order (rowid).
func (s *Store) ListPoemsByBookInDataset(dir, chapter string, limit, offset int) ([]model.Poem, error) {
	rows, err := s.db.Query(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE id LIKE ? AND chapter = ? ORDER BY rowid LIMIT ? OFFSET ?
	`, dir+"-%", chapter, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

// PoemCountByDataset counts the poems of one dataset by its id prefix.
func (s *Store) PoemCountByDataset(dir string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM poems WHERE id LIKE ?`, dir+"-%").Scan(&n)
	return n, err
}

// PoemCountsAllDatasets answers "how many poems per dataset dir" for every
// dir in one grouped pass over the table.
//
// This exists because the settings page asks it for all 18 datasets on every
// open. Calling PoemCountByDataset per dataset is 18 COUNT scans that LIKE
// cannot serve from an index (the pattern is a prefix but the scan still
// walks), which at a 50k corpus costs ~4x the single grouped query.
//
// The grouping key is the id up to the first '-', which is exactly how the
// importers build ids ("<dir>-<file>-<n>", model.Dataset.Dir never contains a
// dash): a key equals a dataset's Dir iff that dataset's PoemCountByDataset
// LIKE 'dir-%' counts those rows. Ids without a dash (none in this corpus)
// collect under the empty key, which no dataset looks up.
func (s *Store) PoemCountsAllDatasets() (map[string]int, error) {
	rows, err := s.db.Query(`
		SELECT substr(id, 1, instr(id, '-') - 1), COUNT(*) FROM poems GROUP BY 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var dir string
		var n int
		if err := rows.Scan(&dir, &n); err != nil {
			return nil, err
		}
		counts[dir] = n
	}
	return counts, rows.Err()
}

// SearchPoems searches the corpus for q, using the FTS5 index when it can
// serve the query and the LIKE scan when it cannot.
//
// The fallback is not a courtesy: the trigram tokenizer cannot match terms
// shorter than trigramMinChars, and it reports that as zero rows rather than
// an error, so a two-character term would silently come back empty and read to
// the user as "nothing matches" while the same text sits in the table.
func (s *Store) SearchPoems(q string, limit, offset int) ([]model.Poem, error) {
	ftsQuery, ok := buildFTSQuery(q)
	if ok {
		poems, err := s.searchFTS(ftsQuery, limit, offset)
		if err == nil {
			return poems, nil
		}
		// Fall through. An error here means the index cannot answer the query
		// (missing table, parser rejection, corrupted index); the LIKE scan is
		// the honest answer, not an empty result set.
	}
	return s.searchLike(q, limit, offset)
}

// trigramMinChars is the shortest term the trigram tokenizer can match: it
// tokenizes into 3-character grams, so a shorter input matches nothing.
const trigramMinChars = 3

// buildFTSQuery turns a raw search string into an FTS5 query poems_fts can
// serve, reporting false when it cannot.
//
// Each whitespace-separated term is sent as a quoted phrase: a quoted string
// is a literal substring with no FTS5 operators in it, so the query can never
// be rejected because of quotes, brackets or other punctuation the user typed.
// Terms are ANDed.
func buildFTSQuery(q string) (string, bool) {
	terms := strings.Fields(q)
	if len(terms) == 0 {
		return "", false
	}
	parts := make([]string, len(terms))
	for i, term := range terms {
		if utf8.RuneCountInString(term) < trigramMinChars {
			return "", false
		}
		parts[i] = ftsPhrase(term)
	}
	return strings.Join(parts, " AND "), true
}

// ftsPhrase quotes one term for MATCH. Inside a quoted string only the double
// quote needs escaping, and it is escaped by doubling it.
func ftsPhrase(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

// searchFTS answers one already-validated FTS5 query. Results are ordered by
// relevance, with rowid as a tiebreaker so pages stay stable.
func (s *Store) searchFTS(ftsQuery string, limit, offset int) ([]model.Poem, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.dynasty, p.author, p.title, p.rhythmic, p.content, p.tags, p.chapter, p.section, p.poem_type
		FROM poems p
		JOIN poems_fts f ON p.rowid = f.rowid
		WHERE f MATCH ?
		ORDER BY rank, p.rowid LIMIT ? OFFSET ?
	`, ftsQuery, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

// searchLike is the whole-corpus scan SearchPoems falls back to. LIKE is
// substring matching, which is the semantics the trigram index matches too, so
// switching paths changes ordering (title vs relevance), never recall.
func (s *Store) searchLike(q string, limit, offset int) ([]model.Poem, error) {
	like := "%" + q + "%"
	rows, err := s.db.Query(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE title LIKE ? OR author LIKE ? OR content LIKE ? OR chapter LIKE ?
		ORDER BY title LIMIT ? OFFSET ?
	`, like, like, like, like, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

// Order-by-rowid random selection: ORDER BY RANDOM() scans and sorts the
// whole candidate set every call (255k rows for RandomPoem); instead the
// unfiltered query picks a random rowid in [1, MAX(rowid)] and index-seeks to
// the first row at or above it. Rowid is implicit (poems has a TEXT primary
// key, no WITHOUT ROWID), so the seek is O(log n) via the rowid b-tree.
// Filtered variants must not reuse this trick — see randomPoemWhere.
//
// ABS(RANDOM() % n) is used (not ABS(RANDOM())) because RANDOM() returns a
// 64-bit signed int and ABS of the minimum value overflows; modulo a small n
// first keeps the operand in range and ABS then yields [1, n].

func (s *Store) RandomPoem() (*model.Poem, error) {
	var p model.Poem
	err := s.db.QueryRow(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE rowid >= (
			SELECT 1 + ABS(RANDOM() % MAX(1, (SELECT COALESCE(MAX(rowid),0) FROM poems)))
		)
		ORDER BY rowid LIMIT 1
	`).Scan(&p.ID, &p.Dynasty, &p.Author, &p.Title, &p.Rhythmic, &p.Content, &p.Tags, &p.Chapter, &p.Section, &p.Type)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) RandomPoemByDynasty(dynasty model.Dynasty) (*model.Poem, error) {
	return s.randomPoemWhere(`dynasty = ?`, string(dynasty))
}

func (s *Store) RandomPoemByType(pType string) (*model.Poem, error) {
	return s.randomPoemWhere(`poem_type = ?`, pType)
}

// randomPoemWhere draws one row uniformly at random from a filtered candidate
// set.
//
// A random rowid is not a random row. P(row i) = (rowid[i+1]-rowid[i]) / MAX,
// so the wider the gap before a row, the more often it is drawn. For the full
// table that gap is ~1 everywhere (imports append, and poems is only ever
// wiped wholesale, never pruned row by row), which is why RandomPoem keeps the
// cheap index seek. A filtered subset is different: its rowids sit in the
// contiguous block that dataset was imported in, so a uniform r in
// [1, MAX(rowid)] lands in the pre-subset gap and the subset's first row is
// drawn up to hundreds of times more often than its last.
//
// Counting the candidates and taking a uniform offset over the count domain
// fixes it: ORDER BY rowid is a fixed total order, so OFFSET k is an exact
// uniform draw. Subsets are thousands of rows, not 255k, so the extra count
// plus offset scan is negligible for a "random poem" button.
func (s *Store) randomPoemWhere(where string, args ...any) (*model.Poem, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM poems WHERE `+where, args...).Scan(&n); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}

	var p model.Poem
	err := s.db.QueryRow(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE `+where+`
		ORDER BY rowid LIMIT 1 OFFSET ?`,
		append(append([]any{}, args...), rand.Intn(n))...).
		Scan(&p.ID, &p.Dynasty, &p.Author, &p.Title, &p.Rhythmic, &p.Content, &p.Tags, &p.Chapter, &p.Section, &p.Type)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ToggleCollection(poemID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(`SELECT 1 FROM collections WHERE poem_id = ?`, poemID).Scan(&exists)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if exists {
		_, err := s.db.Exec(`DELETE FROM collections WHERE poem_id = ?`, poemID)
		return false, err
	}
	_, err = s.db.Exec(`INSERT INTO collections (poem_id) VALUES (?)`, poemID)
	return true, err
}

func (s *Store) IsCollected(poemID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(`SELECT 1 FROM collections WHERE poem_id = ?`, poemID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return exists, err
}

func (s *Store) ListCollections(limit, offset int) ([]model.Poem, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.dynasty, p.author, p.title, p.rhythmic, p.content, p.tags, p.chapter, p.section, p.poem_type
		FROM poems p
		JOIN collections c ON p.id = c.poem_id
		ORDER BY c.created_at DESC LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

// ClearAllCollections removes all collections without touching poems.
// The FTS index needs no separate wipe: the AFTER DELETE trigger clears
// entries as each collection is removed, and collections has no FTS index.
func (s *Store) ClearAllCollections() error {
	_, err := s.db.Exec(`DELETE FROM collections`)
	return err
}

// ClearAllPoems removes all poems and collections. The FTS index needs no
// separate wipe: the AFTER DELETE trigger clears an entry as each poem goes,
// and deleting poems_fts first would make that trigger's 'delete' command fail
// on entries that are already gone.
func (s *Store) ClearAllPoems() error {
	_, err := s.db.Exec(`DELETE FROM collections`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM poems`)
	return err
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) GetMeta(key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// ---- Persisted user preferences (font size, theme, search history) ----

const (
	MetaFontSize      = "pref_font_size"
	MetaTheme         = "pref_theme"
	MetaSearchHistory = "search_history"
)

func (s *Store) GetFontSize() int {
	v, err := s.GetMeta(MetaFontSize)
	if err != nil || v == "" {
		return 18
	}
	n := 0
	for _, c := range v {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	if n < 12 {
		return 12
	}
	if n > 48 {
		return 48
	}
	return n
}

func (s *Store) SetFontSize(n int) error {
	if n < 12 {
		n = 12
	}
	if n > 48 {
		n = 48
	}
	b := []byte{byte('0' + n/10), byte('0' + n%10)}
	return s.SetMeta(MetaFontSize, string(b))
}

func (s *Store) GetTheme() string {
	v, err := s.GetMeta(MetaTheme)
	if err != nil || v == "" {
		return "light"
	}
	if v == "dark" || v == "light" {
		return v
	}
	return "light"
}

func (s *Store) SetTheme(name string) error {
	if name != "dark" && name != "light" {
		name = "light"
	}
	return s.SetMeta(MetaTheme, name)
}

func (s *Store) AddSearchHistory(q string) error {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	hist, _ := s.ListSearchHistory()
	hist = append([]string{q}, hist...)
	seen := map[string]bool{q: true}
	out := []string{q}
	for _, h := range hist[1:] {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	if len(out) > 20 {
		out = out[:20]
	}
	b, _ := json.Marshal(out)
	return s.SetMeta(MetaSearchHistory, string(b))
}

func (s *Store) ListSearchHistory() ([]string, error) {
	v, err := s.GetMeta(MetaSearchHistory)
	if err != nil || v == "" {
		return nil, nil
	}
	var hist []string
	if err := json.Unmarshal([]byte(v), &hist); err != nil {
		return nil, nil
	}
	return hist, nil
}

func (s *Store) ClearSearchHistory() error {
	return s.SetMeta(MetaSearchHistory, "")
}

func scanPoems(rows *sql.Rows) ([]model.Poem, error) {
	var res []model.Poem
	for rows.Next() {
		var p model.Poem
		if err := rows.Scan(&p.ID, &p.Dynasty, &p.Author, &p.Title, &p.Rhythmic, &p.Content, &p.Tags, &p.Chapter, &p.Section, &p.Type); err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

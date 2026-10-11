package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

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

func (s *Store) migrate() error {
	// Check schema version and migrate if needed
	version, err := s.getSchemaVersion()
	if err != nil {
		return err
	}

	if version < 2 {
		// Old schema - need to migrate
		return s.migrateFromOldSchema()
	}

	return nil
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

func (s *Store) migrateFromOldSchema() error {
	// Drop and recreate all tables for a clean start
	dropStmts := []string{
		`DROP TABLE IF EXISTS poems_fts`,
		`DROP TABLE IF EXISTS poems`,
		`DROP TABLE IF EXISTS authors`,
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

CREATE TABLE authors (
	id TEXT PRIMARY KEY,
	name TEXT,
	dynasty TEXT,
	description TEXT
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
		`CREATE INDEX idx_authors_dynasty ON authors(dynasty)`,
		`CREATE INDEX idx_authors_name ON authors(name)`,
	}
	for _, stmt := range indexStmts {
		_, _ = s.db.Exec(stmt)
	}

	// Create FTS table
	_, err = s.db.Exec(`
		CREATE VIRTUAL TABLE poems_fts USING fts5(
			content,
			author,
			title,
			content_rowid=rowid,
			content=poems
		)
	`)
	if err != nil {
		return err
	}

	// Set version
	_, err = s.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', '2')`)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DataDir() string { return s.dataDir }

func (s *Store) Begin() (*sql.Tx, error) {
	return s.db.Begin()
}

func (s *Store) ImportPoems(tx *sql.Tx, poems []model.Poem) error {
	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO poems (id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range poems {
		_, err := stmt.Exec(p.ID, string(p.Dynasty), p.Author, p.Title, p.Rhythmic, p.Content, p.Tags, p.Chapter, p.Section, p.Type)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ImportAuthors(tx *sql.Tx, authors []model.Author) error {
	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO authors (id, name, dynasty, description)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, a := range authors {
		_, err := stmt.Exec(a.ID, a.Name, string(a.Dynasty), a.Desc)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RebuildFTS() error {
	_, err := s.db.Exec(`
		INSERT INTO poems_fts(rowid, content, author, title)
		SELECT rowid, content, author, title FROM poems
		WHERE rowid NOT IN (SELECT rowid FROM poems_fts)
	`)
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

func (s *Store) AuthorCount(dynasty model.Dynasty) (int, error) {
	var query string
	var args []any
	if dynasty == "" {
		query = `SELECT COUNT(DISTINCT author) FROM poems`
	} else {
		query = `SELECT COUNT(DISTINCT author) FROM poems WHERE dynasty = ?`
		args = append(args, string(dynasty))
	}
	var n int
	err := s.db.QueryRow(query, args...).Scan(&n)
	return n, err
}

func (s *Store) ListDynasties() ([]model.Dynasty, error) {
	rows, err := s.db.Query(`SELECT DISTINCT dynasty FROM poems ORDER BY dynasty`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Dynasty
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		res = append(res, model.Dynasty(d))
	}
	return res, rows.Err()
}

func (s *Store) ListAuthors(dynasty model.Dynasty, limit, offset int) ([]model.Author, error) {
	var query string
	var args []any
	if dynasty == "" {
		query = `SELECT id, name, dynasty, description FROM authors ORDER BY name LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	} else {
		query = `SELECT id, name, dynasty, description FROM authors WHERE dynasty = ? ORDER BY name LIMIT ? OFFSET ?`
		args = append(args, string(dynasty), limit, offset)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Author
	for rows.Next() {
		var a model.Author
		if err := rows.Scan(&a.ID, &a.Name, &a.Dynasty, &a.Desc); err != nil {
			return nil, err
		}
		res = append(res, a)
	}
	return res, rows.Err()
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

func (s *Store) SearchPoems(q string, limit, offset int) ([]model.Poem, error) {
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

func (s *Store) SearchPoemsFTS(q string, limit, offset int) ([]model.Poem, error) {
	words := strings.Fields(q)
	if len(words) == 0 {
		return nil, nil
	}
	for i := range words {
		words[i] = words[i] + "*"
	}
	ftsQuery := strings.Join(words, " AND ")
	rows, err := s.db.Query(`
		SELECT p.id, p.dynasty, p.author, p.title, p.rhythmic, p.content, p.tags, p.chapter, p.section, p.poem_type
		FROM poems p
		JOIN poems_fts f ON p.rowid = f.rowid
		WHERE poems_fts MATCH ?
		ORDER BY rank LIMIT ? OFFSET ?
	`, ftsQuery, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoems(rows)
}

func (s *Store) GetPoem(id string) (*model.Poem, error) {
	var p model.Poem
	err := s.db.QueryRow(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems WHERE id = ?
	`, id).Scan(&p.ID, &p.Dynasty, &p.Author, &p.Title, &p.Rhythmic, &p.Content, &p.Tags, &p.Chapter, &p.Section, &p.Type)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Order-by-rowid random selection: ORDER BY RANDOM() scans and sorts the
// whole candidate set every call (255k rows for RandomPoem); instead each
// query below picks a random rowid in [1, MAX(rowid)] and index-seeks to the
// first row at or above it. Rowid is implicit (poems has a TEXT primary key,
// no WITHOUT ROWID), so the seek is O(log n) via the rowid b-tree.
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
	var p model.Poem
	err := s.db.QueryRow(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE dynasty = ?
		AND rowid >= (
			SELECT 1 + ABS(RANDOM() % MAX(1, (SELECT COALESCE(MAX(rowid),0) FROM poems WHERE dynasty = ?)))
		)
		ORDER BY rowid LIMIT 1
	`, string(dynasty), string(dynasty)).Scan(&p.ID, &p.Dynasty, &p.Author, &p.Title, &p.Rhythmic, &p.Content, &p.Tags, &p.Chapter, &p.Section, &p.Type)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) RandomPoemByType(pType string) (*model.Poem, error) {
	var p model.Poem
	err := s.db.QueryRow(`
		SELECT id, dynasty, author, title, rhythmic, content, tags, chapter, section, poem_type FROM poems
		WHERE poem_type = ?
		AND rowid >= (
			SELECT 1 + ABS(RANDOM() % MAX(1, (SELECT COALESCE(MAX(rowid),0) FROM poems WHERE poem_type = ?)))
		)
		ORDER BY rowid LIMIT 1
	`, pType, pType).Scan(&p.ID, &p.Dynasty, &p.Author, &p.Title, &p.Rhythmic, &p.Content, &p.Tags, &p.Chapter, &p.Section, &p.Type)
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

// ClearAllPoems removes all poems, collections and FTS index entries.
func (s *Store) ClearAllPoems() error {
	_, err := s.db.Exec(`DELETE FROM poems_fts`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM collections`)
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

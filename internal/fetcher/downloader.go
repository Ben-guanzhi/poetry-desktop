package fetcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"poetry-desktop/internal/model"
	"poetry-desktop/internal/store"
)

const baseURL = "https://raw.githubusercontent.com/zhangleiDev/chinese-poetry-simplify/main/"

var httpClient = &http.Client{Timeout: 30 * time.Second}

type Progress struct {
	Dataset string
	File    string
	Current int
	Total   int
	Message string
}

type Downloader struct {
	dataDir    string
	store      *store.Store
	onProgress func(Progress)
}

func New(dataDir string, s *store.Store, onProgress func(Progress)) *Downloader {
	return &Downloader{dataDir: dataDir, store: s, onProgress: onProgress}
}

func (d *Downloader) DownloadAndImport(ds model.Dataset) error {
	dir := filepath.Join(d.dataDir, ds.Dir)

	if ds.AutoDir {
		return d.importLocalDataset(ds, dir)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Build file list
	files := ds.PoemFiles
	if len(files) == 0 && ds.PoemPattern != "" {
		files = d.discoverFiles(ds)
	}

	for i, f := range files {
		d.report(ds.Name, f, i, len(files), "下载...")
		data, err := d.downloadFile(ds.Dir, f)
		if err != nil {
			d.report(ds.Name, f, i, len(files), "跳过: "+err.Error())
			continue
		}
		d.report(ds.Name, f, i+1, len(files), "导入...")
		if err := d.importPoems(ds, f, data); err != nil {
			d.report(ds.Name, f, i+1, len(files), "导入失败: "+err.Error())
		}
	}

	d.report(ds.Name, "", len(files), len(files), "重建索引...")
	return d.store.RebuildFTS()
}

// importLocalDataset imports every *.json in a local directory (no download).
// Used for crawled wikisource books placed into the dataset dir by hand.
func (d *Downloader) importLocalDataset(ds model.Dataset, dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("目录为空: 请先用 tools/crawler 抓取书籍，将生成的 JSON 放入 %s", dir)
	}

	imported := 0
	for i, f := range files {
		d.report(ds.Name, f, i, len(files), "导入...")
		data, err := readPayload(filepath.Join(dir, f))
		if err != nil {
			d.report(ds.Name, f, i+1, len(files), "导入失败: "+err.Error())
			continue
		}
		if err := d.importPoems(ds, f, data); err != nil {
			d.report(ds.Name, f, i+1, len(files), "导入失败: "+err.Error())
			continue
		}
		imported++
	}

	d.report(ds.Name, "", len(files), len(files), fmt.Sprintf("已导入 %d/%d 个文件，重建索引...", imported, len(files)))
	return d.store.RebuildFTS()
}

func (d *Downloader) discoverFiles(ds model.Dataset) []string {
	// Pattern datasets (poet.tang.%d.json etc.) hold ~1000 poems per file.
	// The old code probed only the local disk and, on a fresh machine, broke
	// out after the very first missing file — so it downloaded only index 0.
	// Derive the expected range from ds.Count and probe the remote for the
	// actual files, so a fresh install gets the whole dataset.
	files := d.localFiles(ds)
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f] = true
	}

	// Remote probe, stepping through the index space. Stop after the first
	// gap that lies past the count-derived bound; keep probing within it so
	// non-contiguous files are still picked up.
	bound := ((ds.Count + perFile - 1) / perFile) * perFile
	for i := 0; i <= bound; i += perFile {
		name := fmt.Sprintf(ds.PoemPattern, i)
		if seen[name] {
			continue
		}
		if d.remoteExists(ds.Dir, name) {
			seen[name] = true
			files = append(files, name)
		} else if i >= bound-perFile {
			break // a gap at the tail: the dataset ends here
		}
	}

	// Keep the discover result stable for callers that enumerate repeatedly.
	return files
}

// localFiles lists the already-downloaded JSON payloads for a dataset, so
// re-runs never re-download. Shared with discoverFiles and kept separate so
// the disk-only behavior is unit-testable without network access.
func (d *Downloader) localFiles(ds model.Dataset) []string {
	var files []string
	entries, err := os.ReadDir(filepath.Join(d.dataDir, ds.Dir))
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// ds.AuthorFile is the directory's one non-poem payload.
		if e.Name() == ds.AuthorFile {
			continue
		}
		files = append(files, e.Name())
	}
	return files
}

const perFile = 1000

// remoteExists does a cheap HEAD so discovery does not download every file
// just to learn whether it exists. raw.githubusercontent.com answers HEAD
// with 200/404 without transferring a body.
func (d *Downloader) remoteExists(dir, name string) bool {
	req, err := http.NewRequest(http.MethodHead, baseURL+urlEncode(dir)+"/"+urlEncode(name), nil)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// downloadFile makes the named file available locally and returns its complete
// payload, so the importer never opens the file a second time.
func (d *Downloader) downloadFile(dir, name string) ([]byte, error) {
	localPath := filepath.Join(d.dataDir, dir, name)
	// Only a complete file counts as downloaded. An interrupted run may have
	// left a partial .part file, which must not be mistaken for the payload.
	if data, err := readPayload(localPath); err == nil {
		return data, nil
	}
	// Missing or corrupt/truncated leftover: replace it rather than keep
	// importing what is already on disk.

	partPath := localPath + ".part"
	_ = os.Remove(partPath)
	url := baseURL + urlEncode(dir) + "/" + urlEncode(name)
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("not found")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	f, err := os.Create(partPath)
	if err != nil {
		return nil, err
	}
	// Tee the stream: write to the .part file and keep the bytes in hand, so
	// importing needs no read-back.
	var buf bytes.Buffer
	written, err := io.Copy(io.MultiWriter(f, &buf), resp.Body)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(partPath)
		return nil, err
	}
	if closeErr != nil {
		_ = os.Remove(partPath)
		return nil, closeErr
	}
	// Reject a short transfer (interrupted body) instead of promoting a
	// truncated file to the final name.
	if resp.ContentLength > 0 && written != resp.ContentLength {
		_ = os.Remove(partPath)
		return nil, fmt.Errorf("short download: got %d of %d bytes", written, resp.ContentLength)
	}
	if written == 0 {
		_ = os.Remove(partPath)
		return nil, fmt.Errorf("empty download")
	}
	if err := os.Rename(partPath, localPath); err != nil {
		_ = os.Remove(partPath)
		return nil, err
	}
	return buf.Bytes(), nil
}

// readPayload returns a locally complete JSON document, naming the file in the
// error otherwise. Every payload in this repo is a JSON file, so "complete" is
// decidable without parsing it for real: a truncated download (an interrupted
// io.Copy, a half-written buffer) fails json.Valid and is re-fetched or
// skipped instead of poisoning the corpus.
//
// This is the single place that opens a payload file. The downloaders return
// its bytes to the importers, so a file is read exactly once per run instead
// of once to validate and once to unmarshal.
func readPayload(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || !json.Valid(data) {
		return nil, fmt.Errorf("%s 不是完整的 JSON 文件（可能上次下载中断）", filepath.Base(path))
	}
	return data, nil
}

// validPayload reports whether the local file is a complete JSON document.
func (d *Downloader) validPayload(path string) bool {
	_, err := readPayload(path)
	return err == nil
}

func (d *Downloader) importPoems(ds model.Dataset, filename string, data []byte) error {
	tx, err := d.store.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var poems []model.Poem
	switch ds.Type {
	case "poem":
		poems, err = d.parsePoemFormat(ds, filename, data)
	case "ci":
		poems, err = d.parseCiFormat(ds, filename, data)
	case "classic":
		poems, err = d.parseClassicFormat(ds, filename, data)
	default:
		poems, err = d.parsePoemFormat(ds, filename, data)
	}
	if err != nil {
		return err
	}
	if len(poems) > 0 {
		if err := d.store.ImportPoems(tx, poems); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------- Format parsers ----------

func (d *Downloader) parsePoemFormat(ds model.Dataset, filename string, data []byte) ([]model.Poem, error) {
	// Standard poetry format: [{author, paragraphs[], title, id, tags[]}]
	var raw []struct {
		Author     string   `json:"author"`
		Paragraphs []string `json:"paragraphs"`
		Title      string   `json:"title"`
		ID         string   `json:"id"`
		Tags       []string `json:"tags"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	poems := make([]model.Poem, 0, len(raw))
	for i, r := range raw {
		id := r.ID
		if id == "" {
			id = fmt.Sprintf("%s-%s-%d", ds.Dir, filename, i)
		}
		poems = append(poems, model.Poem{
			ID:      id,
			Dynasty: ds.Dynasty,
			Author:  r.Author,
			Title:   r.Title,
			Content: strings.Join(r.Paragraphs, "\n"),
			Tags:    strings.Join(r.Tags, ","),
			Type:    "poem",
		})
	}
	return poems, nil
}

func (d *Downloader) parseCiFormat(ds model.Dataset, filename string, data []byte) ([]model.Poem, error) {
	// Song Ci format: [{author, paragraphs[], rhythmic}]
	var raw []struct {
		Author     string   `json:"author"`
		Paragraphs []string `json:"paragraphs"`
		Rhythmic   string   `json:"rhythmic"`
		Title      string   `json:"title"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	poems := make([]model.Poem, 0, len(raw))
	for i, r := range raw {
		id := fmt.Sprintf("%s-%s-%d", ds.Dir, filename, i)
		title := r.Title
		if title == "" {
			title = r.Rhythmic
		}
		poems = append(poems, model.Poem{
			ID:       id,
			Dynasty:  ds.Dynasty,
			Author:   r.Author,
			Title:    title,
			Rhythmic: r.Rhythmic,
			Content:  strings.Join(r.Paragraphs, "\n"),
			Type:     "ci",
		})
	}
	return poems, nil
}

func (d *Downloader) parseClassicFormat(ds model.Dataset, filename string, data []byte) ([]model.Poem, error) {
	// Book format: {name, chapters: [{chapter, paragraphs[]}]} — output of
	// tools/crawler (wikisource). Each chapter becomes one poem; the book
	// name is stored in Chapter so it can be grouped and browsed by book.
	var bookFormat struct {
		Name     string `json:"name"`
		Chapters []struct {
			Chapter    string   `json:"chapter"`
			Paragraphs []string `json:"paragraphs"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(data, &bookFormat); err == nil && len(bookFormat.Chapters) > 0 {
		bookName := bookFormat.Name
		if bookName == "" {
			bookName = strings.TrimSuffix(filename, ".json")
		}
		poems := make([]model.Poem, 0, len(bookFormat.Chapters))
		for i, ch := range bookFormat.Chapters {
			if ch.Chapter == "" {
				ch.Chapter = fmt.Sprintf("第%d章", i+1)
			}
			poems = append(poems, model.Poem{
				ID:      fmt.Sprintf("%s-%s-%d", ds.Dir, filename, i),
				Dynasty: ds.Dynasty,
				Title:   ch.Chapter,
				Chapter: bookName,
				Content: strings.Join(ch.Paragraphs, "\n"),
				Type:    "text",
			})
		}
		return poems, nil
	}

	// Try different classic text formats
	// 1. Array format: [{chapter, paragraphs[]}]
	var arrFormat []struct {
		Chapter    string   `json:"chapter"`
		Paragraphs []string `json:"paragraphs"`
		Title      string   `json:"title"`
		Section    string   `json:"section"`
		Content    []string `json:"content"`
		Author     string   `json:"author"`
	}
	if err := json.Unmarshal(data, &arrFormat); err == nil {
		poems := make([]model.Poem, 0, len(arrFormat))
		for i, r := range arrFormat {
			id := fmt.Sprintf("%s-%s-%d", ds.Dir, filename, i)
			title := r.Title
			if title == "" {
				title = r.Chapter
			}
			content := strings.Join(r.Paragraphs, "\n")
			if content == "" && len(r.Content) > 0 {
				content = strings.Join(r.Content, "\n")
			}
			section := r.Section
			if section == "" && r.Chapter != "" && r.Title != "" {
				section = r.Chapter
			}
			poems = append(poems, model.Poem{
				ID:      id,
				Dynasty: ds.Dynasty,
				Author:  r.Author,
				Title:   title,
				Content: content,
				Chapter: r.Chapter,
				Section: section,
				Type:    "text",
			})
		}
		return poems, nil
	}

	// 2. Single object format: {chapter, paragraphs[]}
	var objFormat struct {
		Chapter    string   `json:"chapter"`
		Paragraphs []string `json:"paragraphs"`
	}
	if err := json.Unmarshal(data, &objFormat); err == nil && objFormat.Chapter != "" {
		poem := model.Poem{
			ID:      fmt.Sprintf("%s-%s", ds.Dir, filename),
			Dynasty: ds.Dynasty,
			Title:   objFormat.Chapter,
			Content: strings.Join(objFormat.Paragraphs, "\n"),
			Chapter: objFormat.Chapter,
			Type:    "text",
		}
		return []model.Poem{poem}, nil
	}

	return nil, fmt.Errorf("unsupported classic format: %s", filename)
}

func (d *Downloader) report(dataset, file string, current, total int, msg string) {
	if d.onProgress != nil {
		d.onProgress(Progress{Dataset: dataset, File: file, Current: current, Total: total, Message: msg})
	}
}

// ---------- JSON structs ----------
//
// Poem payloads are read as map[string]any and normalised in normalisePoem, so
// no per-dynasty poem struct is needed. The author-file shapes went away with
// the authors table (schema v4).

func urlEncode(s string) string {
	return url.PathEscape(s)
}

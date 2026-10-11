package fetcher

import (
	"os"
	"path/filepath"
	"testing"

	"poetry-desktop/internal/model"
)

func TestValidPayload(t *testing.T) {
	d := &Downloader{}
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"array complete", `[{"a":1}]`, true},
		{"object complete", `{"a":1}`, true},
		{"truncated array", `[{"a":1}`, false},
		{"truncated object", `{"a":1`, false},
		{"empty", ``, false},
		{"garbage", `hello`, false},
		{"just bracket", `[`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.json")
			if err := os.WriteFile(path, []byte(c.data), 0644); err != nil {
				t.Fatal(err)
			}
			if got := d.validPayload(path); got != c.want {
				t.Fatalf("validPayload(%q) = %v, want %v", c.data, got, c.want)
			}
		})
	}
}

// The local-scan half of discovery must keep already-downloaded files so
// re-runs never re-download, and must exclude the authors file.
func TestLocalFilesListsPayloads(t *testing.T) {
	dataDir := t.TempDir()
	dir := "全唐诗"
	if err := os.MkdirAll(filepath.Join(dataDir, dir), 0755); err != nil {
		t.Fatal(err)
	}
	names := []string{"poet.tang.0.json", "poet.tang.1000.json"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dataDir, dir, n), []byte("[]"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, dir, "authors.tang.json"), []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}

	ds := model.Dataset{Dir: dir, AuthorFile: "authors.tang.json"}
	d := &Downloader{dataDir: dataDir}
	files := d.localFiles(ds)
	if len(files) != 2 {
		t.Fatalf("localFiles = %v, want the 2 poem payloads (authors excluded)", files)
	}
	got := make(map[string]bool, len(files))
	for _, f := range files {
		got[f] = true
	}
	for _, n := range names {
		if !got[n] {
			t.Fatalf("localFiles dropped %q; got %v", n, files)
		}
	}
}

// A truncated file that passes a pure size check must not be treated as
// downloaded: validPayload must reject it so a later run re-downloads.
func TestDownloadTreatsTruncatedAsMissing(t *testing.T) {
	dataDir := t.TempDir()
	dir := "全唐诗"
	if err := os.MkdirAll(filepath.Join(dataDir, dir), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, dir, "poet.tang.0.json")
	// A >0-byte but truncated JSON file (simulating an interrupted copy).
	if err := os.WriteFile(path, []byte(`[{"title":`), 0644); err != nil {
		t.Fatal(err)
	}
	d := &Downloader{dataDir: dataDir}
	if d.validPayload(path) {
		t.Fatalf("truncated payload was accepted as complete")
	}
	if d.validPayload(filepath.Join(dataDir, dir, "missing.json")) {
		t.Fatal("non-existent path must be reported invalid")
	}
}

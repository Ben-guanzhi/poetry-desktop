package model

import (
	"fmt"
	"strings"
	"testing"
)

// Preview runs once per visible list row, per UI update: makePoemList's
// UpdateItem calls p.Preview(30) for every row that scrolls into view. The
// cost is per row, but the corpus rows are the ones on screen — a page of
// 500 dataset poems, re-rendered on every navigation.

// previewPage benches one page of list rows at a realistic mix: mostly short
// 近体诗 (40-80 runes), some long 词 and crawler chapters (hundreds of runes).
func previewPage(b *testing.B, content string) {
	poems := make([]Poem, 200)
	for i := range poems {
		poems[i] = Poem{ID: fmt.Sprintf("p%d", i), Content: content}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sink string
		for j := range poems {
			sink = poems[j].Preview(30)
		}
		_ = sink
	}
}

// The modal row: a 律诗 of 56 characters.
func BenchmarkPreviewPageJintee(b *testing.B) {
	content := strings.Repeat("君不见黄河之水天上来奔流到海不复回", 4)
	previewPage(b, content[:56])
}

// A long 词 / crawler chapter: ~1200 runes, where converting the whole
// content to []rune to keep 30 of them is pure waste.
func BenchmarkPreviewPageLongChapter(b *testing.B) {
	content := strings.Repeat("明月几时有把酒问青天不知天上宫阙今夕是何年", 60)
	previewPage(b, content)
}

// The short-row case: content below the budget must come back with no cut
// and, after the fix, no scan past the end and no allocation at all.
func BenchmarkPreviewPageShort(b *testing.B) {
	content := "床前明月光疑是地上霜"
	previewPage(b, content)
}

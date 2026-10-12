package model

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPoemPreviewCutsAtRunes(t *testing.T) {
	p := &Poem{Content: `君不见黄河之水天上来，奔流到海不复回`}

	got := p.Preview(10)
	if got != `君不见黄河之水天上来...` {
		t.Fatalf("Preview(10) = %q", got)
	}
	// Ten characters, not ten bytes: the old byte cut returned about three
	// characters of a ten-character request.
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "...")); n != 10 {
		t.Fatalf("Preview(10) kept %d runes, want 10", n)
	}
}

func TestPoemPreviewShortContentIsUntouched(t *testing.T) {
	for _, c := range []struct {
		content string
		n       int
	}{
		{`国破山河在`, 10},
		{`国破山河在`, 5},
		{"", 10},
	} {
		p := &Poem{Content: c.content}
		if got := p.Preview(c.n); got != c.content {
			t.Fatalf("Preview(%d) of %q = %q, want unchanged", c.n, c.content, got)
		}
	}
}

// Asking for zero characters still signals that something was cut, instead of
// silently rendering nothing.
func TestPoemPreviewZeroBudget(t *testing.T) {
	if got := (&Poem{Content: `国破山河在`}).Preview(0); got != "..." {
		t.Fatalf("Preview(0) of non-empty = %q, want ...", got)
	}
	if got := (&Poem{Content: ""}).Preview(0); got != "" {
		t.Fatalf("Preview(0) of empty = %q, want empty", got)
	}
}

// The bug this replaces: content[:30] sliced bytes, so on Chinese text the cut
// landed inside a code point and the widget rendered a replacement character.
// Every cut point must yield valid UTF-8.
func TestPoemPreviewNeverEmitsInvalidUTF8(t *testing.T) {
	p := &Poem{Content: `明月几时有，把酒问青天。不知天上宫阙，今夕是何年`}
	for n := 1; n <= 40; n++ {
		got := p.Preview(n)
		if !utf8.ValidString(got) {
			t.Fatalf("Preview(%d) = %q is not valid UTF-8", n, got)
		}
	}
}

func TestPoemPreviewMixedWidth(t *testing.T) {
	// abc李白def is 8 runes: three and five characters, not 12 bytes.
	p := &Poem{Content: `abc李白def`}
	if got := p.Preview(4); got != `abc李...` {
		t.Fatalf("Preview(4) = %q, want abc李...", got)
	}
	if got := p.Preview(7); got != `abc李白de...` {
		t.Fatalf("Preview(7) = %q, want abc李白de...", got)
	}
	if got := p.Preview(8); got != `abc李白def` {
		t.Fatalf("Preview(8) = %q, want the whole string", got)
	}
}

// A code point that is already invalid must survive as it is rather than being
// spliced apart: Preview must not invent new invalid sequences.
func TestPoemPreviewPreservesExistingInvalidBytes(t *testing.T) {
	p := &Poem{Content: "ok\xff"}
	got := p.Preview(10)
	if got != p.Content {
		t.Fatalf("Preview(10) = %q, want the content unchanged", got)
	}
	if utf8.ValidString(got) {
		t.Fatal("a lone 0xff should still be reported invalid")
	}
}

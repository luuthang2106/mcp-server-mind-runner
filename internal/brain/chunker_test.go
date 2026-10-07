package brain

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func runes(n int) string { return strings.Repeat("x", n) }

func TestChunkShortParagraphs(t *testing.T) {
	chunks := ChunkText("đoạn một.\n\nđoạn hai.\n\n\nđoạn ba.")
	if len(chunks) != 3 {
		t.Fatalf("chunks=%d, muốn 3", len(chunks))
	}
	for i, c := range chunks {
		if c.Ordinal != i {
			t.Fatalf("ordinal=%d tại vị trí %d", c.Ordinal, i)
		}
		if c.TokenCount != EstTokens(c.Text) {
			t.Fatalf("TokenCount=%d của %q", c.TokenCount, c.Text)
		}
	}
	if chunks[0].Text != "đoạn một." || chunks[1].Text != "đoạn hai." || chunks[2].Text != "đoạn ba." {
		t.Fatalf("chunks=%v", chunks)
	}
}

func TestChunkLongParagraphGreedySentences(t *testing.T) {
	// 3 câu 800 rune trong MỘT đoạn (2500 rune) → 3 chunk, mỗi chunk ≤1000
	s := runes(799) + "."
	text := s + " " + s + " " + s
	chunks := ChunkText(text)
	if len(chunks) != 3 {
		t.Fatalf("chunks=%d, muốn 3: %v", len(chunks), chunks)
	}
	for _, c := range chunks {
		if n := utf8.RuneCountInString(c.Text); n > 1000 {
			t.Fatalf("chunk %d rune > 1000", n)
		}
	}
}

func TestChunkHardCut(t *testing.T) {
	chunks := ChunkText(runes(3000))
	if len(chunks) != 3 {
		t.Fatalf("chunks=%d, muốn 3", len(chunks))
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c.Text); n != 1000 {
			t.Fatalf("chunk[%d] rune=%d, muốn 1000", i, n)
		}
	}
}

func TestChunkWhitespaceOnly(t *testing.T) {
	if chunks := ChunkText("  \n\n \t \n\n \n  "); len(chunks) != 0 {
		t.Fatalf("chunks=%v, muốn rỗng", chunks)
	}
}

func TestChunkDeterministic(t *testing.T) {
	text := "đoạn một ngắn.\n\n" + strings.Repeat("câu dài. ", 300) + "\n\n" + runes(2500)
	a, b := ChunkText(text), ChunkText(text)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("không tất định")
	}
}

// TestChunkDocumentLabels: văn bản có marker → gom đoạn ngắn trong mục, đổi
// mục thì cắt, mỗi chunk mang tiền tố [nhãn] và vẫn ≤1000 rune.
func TestChunkDocumentLabels(t *testing.T) {
	text := "lời nói đầu\n\n⟦trang 1⟧\n\nđoạn a.\n\nđoạn b.\n\n⟦trang 2⟧\n\n" + strings.Repeat("câu dài trên trang hai. ", 100)
	chunks := ChunkText(text)
	if len(chunks) < 4 {
		t.Fatalf("chunks=%d: %v", len(chunks), chunks)
	}
	if chunks[0].Text != "lời nói đầu" {
		t.Fatalf("chunk0=%q", chunks[0].Text)
	}
	if chunks[1].Text != "[trang 1] đoạn a.\n\nđoạn b." {
		t.Fatalf("chunk1=%q", chunks[1].Text)
	}
	for i, c := range chunks[2:] {
		if !strings.HasPrefix(c.Text, "[trang 2] ") {
			t.Fatalf("chunk %d thiếu nhãn: %q", i+2, c.Text)
		}
		if n := utf8.RuneCountInString(c.Text); n > maxChunkRunes {
			t.Fatalf("chunk %d dài %d rune", i+2, n)
		}
		if c.Ordinal != i+2 || c.TokenCount != EstTokens(c.Text) {
			t.Fatalf("chunk %+v", c)
		}
	}
	// văn bản thường (không marker) giữ cách tách cũ
	if c := ChunkText("a\n\nb"); len(c) != 2 {
		t.Fatalf("plain=%v", c)
	}
}

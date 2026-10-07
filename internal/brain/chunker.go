// Package brain là tầng nghiệp vụ bộ nhớ (chunking, write path, recall...).
package brain

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxChunkRunes: trần rune mỗi chunk.
const maxChunkRunes = 1000

// Chunk là một mảnh văn bản để embed; ordinal từ 0, tất định.
type Chunk struct {
	Ordinal    int
	TokenCount int
	Text       string
}

// EstTokens ước lượng token = (số rune + 3) / 4. Nguồn duy nhất cho budget
// briefing và TokenCount của chunk.
func EstTokens(s string) int {
	return (utf8.RuneCountInString(s) + 3) / 4
}

var paraSep = regexp.MustCompile(`\n\s*\n`)

// ChunkText tách văn bản thành các Chunk ≤1000 rune, tất định:
// tách theo đoạn rỗng; đoạn ngắn → 1 chunk; đoạn dài → gom câu (cắt sau
// ". " "! " "? " ".\n") greedy ≤1000 rune; câu đơn >1000 → cắt cứng 1000.
// Trim mọi chunk; bỏ chunk rỗng.
func ChunkText(text string) []Chunk {
	var chunks []Chunk
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			chunks = append(chunks, Chunk{Ordinal: len(chunks), TokenCount: EstTokens(s), Text: s})
		}
	}
	for _, para := range paraSep.Split(text, -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if utf8.RuneCountInString(para) <= maxChunkRunes {
			add(para)
			continue
		}
		for _, part := range packSentences(splitSentences(para)) {
			add(part)
		}
	}
	return chunks
}

// splitSentences cắt SAU dấu . ! ? (khi theo sau là space/xuống dòng),
// bỏ khoảng trắng giữa các câu.
func splitSentences(s string) []string {
	rs := []rune(s)
	var out []string
	start := 0
	for i := 0; i < len(rs); i++ {
		if (rs[i] == '.' || rs[i] == '!' || rs[i] == '?') && i+1 < len(rs) && (rs[i+1] == ' ' || rs[i+1] == '\n') {
			out = append(out, string(rs[start:i+1]))
			j := i + 1
			for j < len(rs) && (rs[j] == ' ' || rs[j] == '\n') {
				j++
			}
			start, i = j, j-1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// packSentences gom câu greedy thành các mảnh ≤1000 rune (nối bằng space);
// câu đơn >1000 rune cắt cứng từng 1000.
func packSentences(sentences []string) []string {
	var out, cur []string
	curLen := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, " "))
			cur, curLen = nil, 0
		}
	}
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if rl := utf8.RuneCountInString(s); rl > maxChunkRunes {
			flush()
			rs := []rune(s)
			for len(rs) > 0 {
				n := maxChunkRunes
				if len(rs) < n {
					n = len(rs)
				}
				if piece := strings.TrimSpace(string(rs[:n])); piece != "" {
					out = append(out, piece)
				}
				rs = rs[n:]
			}
			continue
		}
		if curLen > 0 && curLen+1+utf8.RuneCountInString(s) > maxChunkRunes {
			flush()
		}
		if curLen > 0 {
			curLen++ // space nối
		}
		cur = append(cur, s)
		curLen += utf8.RuneCountInString(s)
	}
	flush()
	return out
}

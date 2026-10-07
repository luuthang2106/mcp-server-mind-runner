package brain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"mind-runner/internal/docread"
	"mind-runner/internal/execx"
	"mind-runner/internal/store"
)

// docReadTimeout: trần thời gian trích chữ (pdftotext/textutil) một file.
const docReadTimeout = 2 * time.Minute

// IngestTextParams tham số nạp file văn bản; Kind "" → "document".
type IngestTextParams struct {
	Path    string
	SpaceID int64
	Kind    string
	Tags    []string // thêm vào tag tự sinh từ tên file
	Title   string   // rỗng → tên file
	Summary string   // tóm tắt 1–2 câu (tuỳ chọn, do agent viết)
}

// ingestKinds: kind nhận từ ingest — transcript/caption chỉ do pipeline media sinh.
var ingestKinds = map[string]bool{
	"document": true, "note": true, "fact": true, "preference": true, "decision": true, "task_hint": true,
}

// FileTag: tag tự sinh từ tên file ("Spec Auth v2.md" → "file:spec-auth-v2")
// để lọc recall theo tài liệu.
func FileTag(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(base) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
			dash = false
		} else if !dash && sb.Len() > 0 {
			sb.WriteByte('-')
			dash = true
		}
	}
	slug := strings.TrimSuffix(sb.String(), "-")
	if slug == "" {
		return ""
	}
	return "file:" + slug
}

// IngestText đọc file và ghi note idempotent (content-hash): nạp lặp cùng nội
// dung không tạo trùng. source = ingest:<path>, không gắn session.
func (b *Brain) IngestText(ctx context.Context, p IngestTextParams) (WriteResult, error) {
	kind := p.Kind
	if kind == "" {
		kind = "document"
	}
	if !ingestKinds[kind] {
		return WriteResult{}, fmt.Errorf("invalid kind for ingest: %q (document|note|fact|preference|decision|task_hint)", kind)
	}
	if fi, err := os.Stat(p.Path); err != nil {
		return WriteResult{}, fmt.Errorf("đọc file: %w", err)
	} else if fi.IsDir() {
		return WriteResult{}, fmt.Errorf("%s là thư mục — nạp từng file", p.Path)
	}
	rctx, cancel := context.WithTimeout(ctx, docReadTimeout)
	defer cancel()
	run := b.run
	if run == nil {
		run = execx.OS{}
	}
	text, err := docread.Read(rctx, run, p.Path)
	if err != nil {
		return WriteResult{}, err
	}
	tags := slices.Clone(p.Tags)
	if ft := FileTag(p.Path); ft != "" && !slices.Contains(tags, ft) {
		tags = append(tags, ft)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = filepath.Base(p.Path)
	}
	return b.WriteNote(ctx, WriteParams{
		SpaceID: p.SpaceID,
		Kind:    kind,
		Text:    text,
		Tags:    tags,
		Source:  "ingest:" + p.Path,
		Meta:    store.NoteMeta{Title: title, Summary: strings.TrimSpace(p.Summary), Ref: p.Path},
	})
}

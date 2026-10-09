package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/execx"
	"mind-runner/internal/store"
)

// Brain là tầng nghiệp vụ bộ nhớ.
type Brain struct {
	st  *store.Store
	eg  *egress.Egress
	cfg *config.Config // nguồn chân lý policy của space ([spaces.policy])

	briefBudget int    // 0 → defaultBriefingBudget
	project     string // project của tiến trình (MCP server theo cwd); "" = chung

	run execx.Runner // lệnh ngoài trích chữ tài liệu (pdftotext…); nil → execx.OS
}

// SetRunner thay runner lệnh ngoài (test dùng fake).
func (b *Brain) SetRunner(r execx.Runner) { b.run = r }

func New(st *store.Store, eg *egress.Egress, cfg *config.Config) *Brain {
	return &Brain{st: st, eg: eg, cfg: cfg}
}

// validKinds: kind hợp lệ (chữ thường) — D13: fact/preference/decision/procedure/
// document vĩnh viễn, note/task_hint/transcript/caption là sự kiện (purge theo retention).
var validKinds = map[string]bool{
	"note": true, "fact": true, "preference": true, "decision": true, "document": true,
	"procedure": true, "task_hint": true, "transcript": true, "caption": true,
}

// ValidKind: kind có hợp lệ không (dùng để kiểm filter kinds của recall).
func ValidKind(k string) bool { return validKinds[k] }

// WriteParams tham số ghi note.
type WriteParams struct {
	SpaceID   int64
	Kind      string
	Text      string
	Tags      []string
	Source    string
	SessionID *string
	Meta      store.NoteMeta // trường có cấu trúc tuỳ kind (why/who/when/ref…); rỗng = không có
	// Project: nhãn project; "" → project của Brain (SetProject). Dùng "-" để
	// ép không gắn project (nạp tài liệu chung).
	Project string
}

// WriteResult Fresh=false nghĩa là "ôn lại" (note đã tồn tại).
type WriteResult struct {
	NoteID int64
	Fresh  bool
}

// WriteNote ghi idempotent: note mới → chunk + 1 job embed_chunk{note_id};
// ghi lại cùng nội dung → chỉ gia hạn updated_at, không chunk/job mới.
func (b *Brain) WriteNote(ctx context.Context, p WriteParams) (WriteResult, error) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return WriteResult{}, errors.New("text rỗng")
	}
	if !validKinds[p.Kind] {
		return WriteResult{}, fmt.Errorf("kind không hợp lệ: %q", p.Kind)
	}
	now := time.Now()
	id, fresh, err := b.st.UpsertNote(ctx, &store.Note{
		SpaceID: p.SpaceID, Kind: p.Kind, Text: text, Tags: p.Tags,
		Source: p.Source, SessionID: p.SessionID, CreatedAt: now, UpdatedAt: now,
		Meta: p.Meta, Project: b.projectFor(p.Project),
	})
	if err != nil {
		return WriteResult{}, err
	}
	if !fresh {
		return WriteResult{NoteID: id, Fresh: false}, nil
	}
	// chunk bản ĐÃ redact: chunk được embed/rerank gửi ra ngoài; redact từng
	// chunk sau khi cắt không bắt được private key bị chia (chunk giữa thuần base64).
	chunks := ChunkText(egress.Redact(embedText(text, p.Meta)).Text)
	sc := make([]store.Chunk, len(chunks))
	for i, c := range chunks {
		sc[i] = store.Chunk{Ordinal: c.Ordinal, Text: c.Text, TokenCount: c.TokenCount}
	}
	if err := b.st.InsertChunks(ctx, id, sc); err != nil {
		return WriteResult{}, err
	}
	if _, err := b.st.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": id}, now); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{NoteID: id, Fresh: true}, nil
}

// EmbedText: văn bản đại diện note khi embed (dùng chung cho dò trùng).
func EmbedText(text string, m store.NoteMeta) string { return embedText(text, m) }

// embedText: nội dung được chunk/embed/FTS. Why đi kèm text vì "tại sao" là
// thứ hay được hỏi lại ("vì sao chọn X?") — các trường khác (who/ref/as_of)
// chỉ là metadata để trích dẫn, đưa vào sẽ làm loãng embedding.
func embedText(text string, m store.NoteMeta) string {
	why := strings.TrimSpace(m.Why)
	if why == "" || strings.Contains(text, why) {
		return text
	}
	return text + "\nWhy: " + why
}

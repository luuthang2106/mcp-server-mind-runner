package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type FTSHit struct {
	ChunkID int64
	NoteID  int64
}

var ftsTokens = regexp.MustCompile(`[\p{L}\p{N}]+`)

// SanitizeFTSQuery: tokens = chuỗi unicode letter/digit (giữ dấu Việt); mỗi
// token bọc "..." (escape " → ""); join " OR "; rỗng → "".
func SanitizeFTSQuery(q string) string {
	toks := ftsTokens.FindAllString(q, -1)
	quoted := make([]string, len(toks))
	for i, t := range toks {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR ")
}

// NoteFilter: điều kiện lọc note dùng chung cho FTS và vector (alias bảng notes
// là n). Rỗng = mọi space, mọi tag, mọi kind, chỉ note còn hiệu lực.
type NoteFilter struct {
	SpaceIDs          []int64
	Tags              []string // AND — note phải mang đủ
	Kinds             []string // OR — rỗng = mọi kind
	IncludeSuperseded bool     // false = ẩn note status superseded
	Project           string   // "" = mọi project; khác rỗng = chỉ note của project này
}

// Key: chuỗi tất định đại diện filter (khoá cache vector).
func (f NoteFilter) Key() string {
	var b strings.Builder
	for _, id := range f.SpaceIDs {
		fmt.Fprintf(&b, "%d,", id)
	}
	b.WriteString("|" + strings.Join(f.Tags, "\x00") + "|" + strings.Join(f.Kinds, ","))
	if f.IncludeSuperseded {
		b.WriteString("|all")
	}
	if f.Project != "" {
		b.WriteString("|p=" + f.Project)
	}
	return b.String()
}

// sql: mệnh đề AND (bắt đầu bằng " AND ...") + args, luôn loại note xoá mềm.
func (f NoteFilter) sql() (string, []any) {
	q := ` AND n.deleted_at IS NULL`
	var args []any
	if !f.IncludeSuperseded {
		q += ` AND n.status = 'active'`
	}
	if len(f.SpaceIDs) > 0 {
		q += ` AND n.space_id IN (` + placeholders(len(f.SpaceIDs)) + `)`
		for _, id := range f.SpaceIDs {
			args = append(args, id)
		}
	}
	if len(f.Kinds) > 0 {
		q += ` AND n.kind IN (` + placeholders(len(f.Kinds)) + `)`
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Project != "" {
		q += ` AND n.project = ?`
		args = append(args, f.Project)
	}
	for _, tag := range f.Tags {
		q += ` AND EXISTS (SELECT 1 FROM json_each(n.tags) WHERE json_each.value = ?)`
		args = append(args, tag)
	}
	return q, args
}

// SearchFTS: BM25 trên chunks_fts theo filter. Query không có token hợp lệ → rỗng.
func (s *Store) SearchFTS(ctx context.Context, query string, f NoteFilter, limit int) ([]FTSHit, error) {
	match := SanitizeFTSQuery(query)
	if match == "" {
		return nil, nil
	}
	cond, fargs := f.sql()
	q := `SELECT c.id, c.note_id FROM chunks_fts f
	      JOIN chunks c ON c.id = f.rowid
	      JOIN notes n ON n.id = c.note_id
	      WHERE chunks_fts MATCH ?` + cond
	args := append([]any{match}, fargs...)
	q += ` ORDER BY bm25(chunks_fts) LIMIT ?`
	args = append(args, limit)

	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FTSHit
	for rows.Next() {
		var h FTSHit
		if err := rows.Scan(&h.ChunkID, &h.NoteID); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Note tương ứng một dòng bảng notes.
type Note struct {
	ID          int64
	SpaceID     int64
	Kind        string
	Text        string
	Tags        []string
	Source      string
	SessionID   *string
	ContentHash string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
	// Meta: trường có cấu trúc tuỳ loại (xem NoteMeta). Rỗng = không có.
	Meta NoteMeta
	// Status/SupersededBy: legacy — cơ chế supersede đã bỏ (mọi thay thế xoá
	// cứng); giữ cột để đọc row cũ tới khi purge (1b) quét sạch.
	Status       string
	SupersededBy *int64
	// Project: nhãn tự động theo thư mục làm việc (tên gốc git); "" = chung.
	Project string
}

// NoteMeta: trường có cấu trúc của note — mọi trường tuỳ chọn, chỉ điền khi
// người dùng nói ra / ngữ cảnh rõ ràng. Không đưa vào content_hash: ghi lại
// cùng text kèm trường mới = bổ sung (merge), không tạo note trùng.
type NoteMeta struct {
	Why          string   `json:"why,omitempty"`          // lý do / bối cảnh (decision: bắt buộc)
	Who          []string `json:"who,omitempty"`          // người liên quan / người quyết / tác giả / người họp
	When         string   `json:"when,omitempty"`         // ngày sự việc / ngày quyết / ngày họp (YYYY-MM-DD)
	AsOf         string   `json:"as_of,omitempty"`        // fact đúng tại ngày này
	Ref          string   `json:"ref,omitempty"`          // nguồn người đọc được: cuộc họp, tài liệu, URL, câu nói
	Alternatives []string `json:"alternatives,omitempty"` // decision: phương án đã cân nhắc và loại
	Scope        string   `json:"scope,omitempty"`        // preference: áp dụng khi nào
	Title        string   `json:"title,omitempty"`        // document: tiêu đề
	Summary      string   `json:"summary,omitempty"`      // document: tóm tắt ngắn
}

// IsZero: meta không có trường nào.
func (m NoteMeta) IsZero() bool {
	return m.Why == "" && len(m.Who) == 0 && m.When == "" && m.AsOf == "" && m.Ref == "" &&
		len(m.Alternatives) == 0 && m.Scope == "" && m.Title == "" && m.Summary == ""
}

// noteCols: cột SELECT chuẩn cho scanNote.
const noteCols = `id, space_id, kind, text, tags, source, session_id, content_hash, created_at, updated_at, deleted_at, meta, status, superseded_by, project`

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// UpsertNote ghi idempotent theo UNIQUE(space_id, content_hash) — không SELECT trước.
// Ghi lại cùng nội dung = "ôn lại": updated_at mới, deleted_at hồi sinh.
// fresh = dòng mới tạo (created_at = updated_at).
func (s *Store) UpsertNote(ctx context.Context, n *Note) (int64, bool, error) {
	n.ContentHash = sha256hex(fmt.Sprintf("%d|%s|%s", n.SpaceID, n.Kind, n.Text))
	tags := "[]"
	if len(n.Tags) > 0 {
		data, err := json.Marshal(n.Tags)
		if err != nil {
			return 0, false, err
		}
		tags = string(data)
	}
	var sessionID any
	if n.SessionID != nil {
		sessionID = *n.SessionID
	}
	meta := "{}"
	if !n.Meta.IsZero() {
		data, err := json.Marshal(n.Meta)
		if err != nil {
			return 0, false, err
		}
		meta = string(data)
	}
	var id int64
	var fresh int
	// Ghi lại: meta merge (trường mới ghi đè, trường cũ giữ), tags hợp nhất
	// không trùng, và row legacy status='superseded' được HỒI SINH về active —
	// ghi lại y hệt không bao giờ làm mất nội dung (purge quét sạch legacy sau).
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO notes(space_id,kind,text,tags,source,session_id,content_hash,created_at,updated_at,meta,project)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(space_id, content_hash)
		 DO UPDATE SET updated_at=excluded.updated_at, deleted_at=NULL,
		   status='active', superseded_by=NULL,
		   project=COALESCE(notes.project, excluded.project),
		   meta=json_patch(notes.meta, excluded.meta),
		   tags=(SELECT json_group_array(value) FROM (
		     SELECT value FROM json_each(notes.tags) UNION SELECT value FROM json_each(excluded.tags)))
		 RETURNING id, created_at = updated_at`,
		n.SpaceID, n.Kind, n.Text, tags, n.Source, sessionID, n.ContentHash,
		ts(n.CreatedAt), ts(n.UpdatedAt), meta, nullStr(n.Project)).Scan(&id, &fresh)
	if err != nil {
		return 0, false, err
	}
	s.BumpGen()
	return id, fresh == 1, nil
}

// FetchNote đọc note theo id (kể cả đã xoá mem).
func (s *Store) FetchNote(ctx context.Context, id int64) (*Note, error) {
	n, err := scanNote(s.DB().QueryRowContext(ctx,
		`SELECT `+noteCols+` FROM notes WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// scanNote đọc một dòng notes từ *sql.Row / *sql.Rows.
func scanNote(sc rowScanner) (Note, error) {
	var n Note
	var tags, createdAt, updatedAt, meta string
	var sessionID, deletedAt sql.NullString
	var supersededBy sql.NullInt64
	var project sql.NullString
	if err := sc.Scan(&n.ID, &n.SpaceID, &n.Kind, &n.Text, &tags, &n.Source,
		&sessionID, &n.ContentHash, &createdAt, &updatedAt, &deletedAt,
		&meta, &n.Status, &supersededBy, &project); err != nil {
		return Note{}, err
	}
	n.Project = project.String
	if meta != "" && meta != "{}" {
		// meta hỏng không làm hỏng cả note — bỏ qua trường.
		_ = json.Unmarshal([]byte(meta), &n.Meta)
	}
	if supersededBy.Valid {
		v := supersededBy.Int64
		n.SupersededBy = &v
	}
	if tags != "" {
		if err := json.Unmarshal([]byte(tags), &n.Tags); err != nil {
			return Note{}, err
		}
	}
	if sessionID.Valid {
		n.SessionID = &sessionID.String
	}
	n.CreatedAt, n.UpdatedAt = parseTS(createdAt), parseTS(updatedAt)
	if deletedAt.Valid {
		t := parseTS(deletedAt.String)
		n.DeletedAt = &t
	}
	return n, nil
}

// SoftDeleteNote xoá mềm: recall/briefing ẩn ngay (BumpGen để cache vector cũ
// vô hiệu), purge xoá thật (M5.5). false = không có note hoặc đã xoá trước đó.
func (s *Store) SoftDeleteNote(ctx context.Context, id int64, now time.Time) (bool, error) {
	res, err := s.DB().ExecContext(ctx,
		`UPDATE notes SET deleted_at=? WHERE id=? AND deleted_at IS NULL`, ts(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		s.BumpGen()
	}
	return n > 0, nil
}

// AllNotesForExport: mọi note chưa xoá mềm (mọi space/kind), theo id.
func (s *Store) AllNotesForExport(ctx context.Context) ([]Note, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT `+noteCols+` FROM notes WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NotesByKind: note theo kind chưa xoá mềm và còn hiệu lực (status active);
// since khác zero → updated_at >= since; sắp updated_at DESC.
func (s *Store) NotesByKind(ctx context.Context, spaceID int64, kind string, since time.Time, limit int) ([]Note, error) {
	q := `SELECT ` + noteCols + `
	      FROM notes WHERE space_id=? AND kind=? AND deleted_at IS NULL AND status='active'`
	args := []any{spaceID, kind}
	if !since.IsZero() {
		q += ` AND updated_at >= ?`
		args = append(args, ts(since))
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ActiveNotesByKinds: note chưa xoá mềm, status active, kind trong danh sách
// của space; sắp theo id (thứ tự tất định cho batch LLM của consolidate).
func (s *Store) ActiveNotesByKinds(ctx context.Context, spaceID int64, kinds []string) ([]Note, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	q := `SELECT ` + noteCols + ` FROM notes
	      WHERE space_id=? AND deleted_at IS NULL AND status='active' AND kind IN (` + placeholders(len(kinds)) + `)
	      ORDER BY id`
	args := []any{spaceID}
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ErrNoteNotFound: note không tồn tại hoặc đã xoá mềm.
var ErrNoteNotFound = errors.New("note không tồn tại hoặc đã bị xoá")

// HardDeleteNotes xoá cứng cả chuỗi của các note (relations → embeddings →
// chunks → notes; trigger dọn chunks_fts) trong MỘT transaction BEGIN
// IMMEDIATE. Dùng khi thay thế ký ức (remember supersedes, dedupe lúc
// extract, consolidate). Không có xoá mềm ở đây — phục hồi chỉ từ backup
// ngày. Trả số note đã xoá; id không tồn tại → bỏ qua, không lỗi.
func (s *Store) HardDeleteNotes(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close() // rollback defer đăng ký sau → chạy trước
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	_, notes, err := deleteNotesChain(ctx, conn, ids)
	if err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, err
	}
	committed = true
	s.BumpGen()
	return notes, nil
}

// NotesCreatedBetween: note (chưa xoá, mọi status) tạo trong [from, to) của
// space, chỉ các source trong sources (rỗng = mọi source); cũ trước.
func (s *Store) NotesCreatedBetween(ctx context.Context, spaceID int64, from, to time.Time, sources []string, limit int) ([]Note, error) {
	q := `SELECT ` + noteCols + ` FROM notes
	      WHERE space_id=? AND deleted_at IS NULL AND created_at >= ? AND created_at < ?`
	args := []any{spaceID, ts(from), ts(to)}
	if len(sources) > 0 {
		q += ` AND source IN (?` + strings.Repeat(", ?", len(sources)-1) + `)`
		for _, src := range sources {
			args = append(args, src)
		}
	}
	q += ` ORDER BY created_at, id LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// nullStr: "" → NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// NoteProjects: project của các note (id không có → không có trong map).
func (s *Store) NoteProjects(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.DB().QueryContext(ctx, `SELECT id, COALESCE(project,'') FROM notes WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

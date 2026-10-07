package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// InsertRaw lưu delta thô (gzip) với retained_until = now + rawDays.
// rawDays truyền vào — store không đọc config.
func (s *Store) InsertRaw(ctx context.Context, sessionID string, seq int, gzipBlob []byte, rawDays int, now time.Time) (int64, error) {
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO session_raw(session_id, seq, content, at, retained_until) VALUES(?,?,?,?,?)`,
		sessionID, seq, gzipBlob, ts(now), ts(now.AddDate(0, 0, rawDays)))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Raw một dòng session_raw; Content là gzip blob thô.
type Raw struct {
	ID        int64
	SessionID string
	Seq       int
	Content   []byte
	At        time.Time
}

// GetRaw đọc một delta thô theo id.
func (s *Store) GetRaw(ctx context.Context, id int64) (Raw, error) {
	var r Raw
	var at string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT id, session_id, seq, content, at FROM session_raw WHERE id=?`, id).
		Scan(&r.ID, &r.SessionID, &r.Seq, &r.Content, &at); err != nil {
		return Raw{}, err
	}
	r.At = parseTS(at)
	return r, nil
}

// RawsForSession đọc toàn bộ delta của session theo seq tăng dần.
func (s *Store) RawsForSession(ctx context.Context, sessionID string) ([]Raw, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id, session_id, seq, content, at FROM session_raw WHERE session_id=? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Raw
	for rows.Next() {
		var r Raw
		var at string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Seq, &r.Content, &at); err != nil {
			return nil, err
		}
		r.At = parseTS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// NextRawSeq trả seq kế tiếp cho session (bắt đầu từ 1).
func (s *Store) NextRawSeq(ctx context.Context, sessionID string) (int, error) {
	var maxSeq sql.NullInt64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT MAX(seq) FROM session_raw WHERE session_id=?`, sessionID).Scan(&maxSeq); err != nil {
		return 0, err
	}
	return int(maxSeq.Int64) + 1, nil
}

// ErrOffsetMoved: offset transcript trong DB đã khác giá trị mong đợi —
// process khác (Stop và SessionEnd chạy gần nhau) đã chốt delta này rồi.
var ErrOffsetMoved = errors.New("transcript offset đã bị process khác cập nhật")

// AppendRaw chốt một delta trong MỘT transaction (BEGIN IMMEDIATE):
// seq = MAX(seq)+1 → INSERT session_raw → cập nhật transcript_offset (chỉ khi
// offset hiện tại == expectOffset, nếu expectOffset >= 0) → đảm bảo phiên có
// một job extract_session đang chờ (gộp: đã có job queued của phiên thì giữ
// nguyên run_after của nó; chưa có → tạo mới run_after = extractAfter).
// Không có race seq/offset giữa các hook, không có trạng thái nửa vời.
// expectOffset < 0: không kiểm/cập nhật offset (spool định dạng cũ).
func (s *Store) AppendRaw(ctx context.Context, sessionID string, gzipBlob []byte, rawDays int, now, extractAfter time.Time, expectOffset, newOffset int64) (int64, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if expectOffset >= 0 {
		res, err := conn.ExecContext(ctx,
			`UPDATE sessions SET transcript_offset=? WHERE id=? AND transcript_offset=?`,
			newOffset, sessionID, expectOffset)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, ErrOffsetMoved
		}
	}
	var maxSeq sql.NullInt64
	if err := conn.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM session_raw WHERE session_id=?`, sessionID).Scan(&maxSeq); err != nil {
		return 0, err
	}
	res, err := conn.ExecContext(ctx,
		`INSERT INTO session_raw(session_id, seq, content, at, retained_until) VALUES(?,?,?,?,?)`,
		sessionID, maxSeq.Int64+1, gzipBlob, ts(now), ts(now.AddDate(0, 0, rawDays)))
	if err != nil {
		return 0, err
	}
	rawID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	payload := extractPayload(sessionID)
	nowS := ts(now)
	// Chỉ gộp vào job 'queued' — job đang 'running' có thể đã chọn xong raw,
	// raw mới cần job kế tiếp; job 'failed' giữ backoff riêng nhưng vẫn gom
	// được raw mới khi chạy lại, nên cũng gộp.
	var pending int64
	err = conn.QueryRowContext(ctx,
		`SELECT id FROM jobs WHERE type='extract_session' AND payload=? AND state IN ('queued','failed') LIMIT 1`,
		payload).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO jobs(type,payload,state,attempts,run_after,created_at,updated_at)
			 VALUES('extract_session',?,'queued',0,?,?,?)`, payload, ts(extractAfter), nowS, nowS); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, err
	}
	committed = true
	return rawID, nil
}

func extractPayload(sessionID string) string {
	b, _ := json.Marshal(map[string]string{"session_id": sessionID})
	return string(b)
}

// ExpediteExtract đưa job extract đang chờ của phiên lên chạy ngay (phiên
// kết thúc — không đợi hết thời gian gộp). Job failed giữ nguyên backoff.
func (s *Store) ExpediteExtract(ctx context.Context, sessionID string, now time.Time) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE jobs SET run_after=?, updated_at=? WHERE type='extract_session' AND payload=? AND state='queued' AND run_after > ?`,
		ts(now), ts(now), extractPayload(sessionID), ts(now))
	return err
}

// PendingRaws trả các raw chưa extract của phiên, theo seq.
func (s *Store) PendingRaws(ctx context.Context, sessionID string) ([]Raw, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id, session_id, seq, content, at FROM session_raw
		  WHERE session_id=? AND extracted_at IS NULL ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Raw
	for rows.Next() {
		var r Raw
		var at string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Seq, &r.Content, &at); err != nil {
			return nil, err
		}
		r.At = parseTS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkRawsExtracted đánh dấu các raw đã được extract (retry không gửi lại).
func (s *Store) MarkRawsExtracted(ctx context.Context, ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	q := `UPDATE session_raw SET extracted_at=? WHERE extracted_at IS NULL AND id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
	args := []any{ts(now)}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.DB().ExecContext(ctx, q, args...)
	return err
}

package store

import (
	"context"
	"database/sql"
	"time"
)

// Session tương ứng một dòng bảng sessions.
type Session struct {
	ID               string
	Client           string
	SpaceID          int64
	TranscriptPath   *string
	TranscriptOffset int64
	LastSeenAt       time.Time
}

// UpsertSessionStart tạo session mới hoặc cập nhật khi resume (cùng id).
// transcript_path mới NULL → giữ giá trị cũ.
func (s *Store) UpsertSessionStart(ctx context.Context, id, client string, spaceID int64, transcriptPath *string, now time.Time) error {
	var tp any
	if transcriptPath != nil {
		tp = *transcriptPath
	}
	_, err := s.DB().ExecContext(ctx,
		`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at, transcript_path, transcript_offset)
		 VALUES(?,?,?,?,?,?,0)
		 ON CONFLICT(id) DO UPDATE SET
		   last_seen_at=excluded.last_seen_at,
		   transcript_path=COALESCE(excluded.transcript_path, sessions.transcript_path)`,
		id, client, spaceID, ts(now), ts(now), tp)
	return err
}

// TouchSession gia hạn last_seen_at.
func (s *Store) TouchSession(ctx context.Context, id string, now time.Time) error {
	_, err := s.DB().ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE id=?`, ts(now), id)
	return err
}

// GetSession đọc session theo id.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	row := s.DB().QueryRowContext(ctx,
		`SELECT id, client, space_id, transcript_path, transcript_offset, last_seen_at
		 FROM sessions WHERE id=?`, id)
	return scanSession(row)
}

// LatestSession trả session gần nhất của client trong space; chưa có → (nil, nil).
func (s *Store) LatestSession(ctx context.Context, client string, spaceID int64) (*Session, error) {
	row := s.DB().QueryRowContext(ctx,
		`SELECT id, client, space_id, transcript_path, transcript_offset, last_seen_at
		 FROM sessions WHERE client=? AND space_id=? ORDER BY last_seen_at DESC, id DESC LIMIT 1`,
		client, spaceID)
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return sess, err
}

func scanSession(row *sql.Row) (*Session, error) {
	var sess Session
	var tp sql.NullString
	var lastSeen string
	if err := row.Scan(&sess.ID, &sess.Client, &sess.SpaceID, &tp, &sess.TranscriptOffset, &lastSeen); err != nil {
		return nil, err
	}
	if tp.Valid {
		sess.TranscriptPath = &tp.String
	}
	sess.LastSeenAt = parseTS(lastSeen)
	return &sess, nil
}

// UpdateTranscriptOffset ghi vị trí đã đọc của transcript.
func (s *Store) UpdateTranscriptOffset(ctx context.Context, id string, offset int64) error {
	_, err := s.DB().ExecContext(ctx, `UPDATE sessions SET transcript_offset=? WHERE id=?`, offset, id)
	return err
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Media là row bảng media (subdomain Media/Omni).
type Media struct {
	ID         int64
	SpaceID    int64
	SHA256     string
	Kind       string // audio | image | video
	Path       string // tương đối trong media/ (video: file audio đã trích)
	Bytes      int64
	DurationS  *float64 // audio/video nếu biết
	Transcript *string  // audio/video: transcript; image: caption
	Model      *string
	Status     string // queued|processing|done|failed|dead
	Source     string
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const mediaCols = `id, space_id, sha256, kind, path, bytes, duration_s, transcript, model, status, source, last_error, created_at, updated_at`

func scanMedia(sc rowScanner) (*Media, error) {
	var m Media
	var duration sql.NullFloat64
	var transcript, model sql.NullString
	var createdAt, updatedAt string
	if err := sc.Scan(&m.ID, &m.SpaceID, &m.SHA256, &m.Kind, &m.Path, &m.Bytes,
		&duration, &transcript, &model, &m.Status, &m.Source, &m.LastError, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if duration.Valid {
		m.DurationS = &duration.Float64
	}
	if transcript.Valid {
		m.Transcript = &transcript.String
	}
	if model.Valid {
		m.Model = &model.String
	}
	m.CreatedAt = parseTS(createdAt)
	m.UpdatedAt = parseTS(updatedAt)
	return &m, nil
}

// InsertMedia ghi row mới; dedupe theo UNIQUE(space_id, sha256) — trùng thì
// created=false, id=0 (caller re-select theo (space, sha)).
func (s *Store) InsertMedia(ctx context.Context, m *Media, now time.Time) (int64, bool, error) {
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO media(space_id, sha256, kind, path, bytes, duration_s, transcript, model, status, source, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(space_id, sha256) DO NOTHING`,
		m.SpaceID, m.SHA256, m.Kind, m.Path, m.Bytes, m.DurationS,
		m.Transcript, m.Model, m.Status, m.Source, ts(now), ts(now))
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	s.BumpGen()
	return id, true, nil
}

// MediaBySHA trả row theo (space, sha); không có → (nil, nil).
func (s *Store) MediaBySHA(ctx context.Context, spaceID int64, sha string) (*Media, error) {
	m, err := scanMedia(s.DB().QueryRowContext(ctx,
		`SELECT `+mediaCols+` FROM media WHERE space_id=? AND sha256=?`, spaceID, sha))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// MediaByID trả row theo id; không có → (nil, nil).
func (s *Store) MediaByID(ctx context.Context, id int64) (*Media, error) {
	m, err := scanMedia(s.DB().QueryRowContext(ctx,
		`SELECT `+mediaCols+` FROM media WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// UpdateMediaStatus đổi status (+ lý do nếu có); lastErr nil → giữ lý do cũ.
func (s *Store) UpdateMediaStatus(ctx context.Context, id int64, status string, lastErr *string, now time.Time) error {
	var err error
	if lastErr != nil {
		_, err = s.DB().ExecContext(ctx,
			`UPDATE media SET status=?, last_error=?, updated_at=? WHERE id=?`, status, *lastErr, ts(now), id)
	} else {
		_, err = s.DB().ExecContext(ctx,
			`UPDATE media SET status=?, updated_at=? WHERE id=?`, status, ts(now), id)
	}
	if err != nil {
		return err
	}
	s.BumpGen()
	return nil
}

// SetMediaTranscript ghi transcript/caption + model omni đã dùng.
func (s *Store) SetMediaTranscript(ctx context.Context, id int64, transcript, model string, now time.Time) error {
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE media SET transcript=?, model=?, updated_at=? WHERE id=?`,
		transcript, model, ts(now), id); err != nil {
		return fmt.Errorf("set transcript media %d: %w", id, err)
	}
	s.BumpGen()
	return nil
}

// ClearMediaPath đánh dấu file gốc đã xoá chủ đích (keep_originals=false):
// path=” nhưng row + transcript giữ nguyên. Doctor phân biệt path rỗng
// (bình thường) với path còn mà file mất (bất thường).
func (s *Store) ClearMediaPath(ctx context.Context, id int64, now time.Time) error {
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE media SET path='', updated_at=? WHERE id=?`, ts(now), id); err != nil {
		return fmt.Errorf("clear path media %d: %w", id, err)
	}
	s.BumpGen()
	return nil
}

// MediaMissingPaths trả các path non-empty mà file không còn trong mediaDir
// (doctor dùng để warn). So sánh trên đĩa — không đụng row.
func (s *Store) MediaMissingPaths(ctx context.Context, mediaDir string) ([]string, error) {
	rows, err := s.DB().QueryContext(ctx, `SELECT path FROM media WHERE path != '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(mediaDir, p)); os.IsNotExist(err) {
			missing = append(missing, p)
		}
	}
	return missing, rows.Err()
}

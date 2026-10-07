package store

import (
	"context"
	"time"
)

// Episode một dòng bảng episodes (tóm tắt phiên).
type Episode struct {
	ID        int64
	SessionID string
	Seq       int
	Summary   string
	At        time.Time
}

// InsertEpisode thêm tóm tắt với seq = COALESCE(MAX(seq),0)+1 của session.
func (s *Store) InsertEpisode(ctx context.Context, sessionID, summary string, now time.Time) (int64, error) {
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO episodes(session_id, seq, summary, at)
		 SELECT ?, COALESCE(MAX(seq),0)+1, ?, ? FROM episodes WHERE session_id=?`,
		sessionID, summary, ts(now), sessionID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RecentEpisodes: episode gần nhất của space (JOIN sessions), at DESC.
func (s *Store) RecentEpisodes(ctx context.Context, spaceID int64, limit int) ([]Episode, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT e.id, e.session_id, e.seq, e.summary, e.at FROM episodes e
		 JOIN sessions s ON s.id = e.session_id
		 WHERE s.space_id=? ORDER BY e.at DESC LIMIT ?`, spaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var e Episode
		var at string
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Summary, &at); err != nil {
			return nil, err
		}
		e.At = parseTS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

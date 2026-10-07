package store

import (
	"context"
	"database/sql"
	"time"
)

// Relation một dòng bảng relations.
type Relation struct {
	ID           int64
	SpaceID      int64
	FromRef      string
	ToRef        string
	RelType      string
	SourceNoteID *int64
	CreatedAt    time.Time
}

// InsertRelation ghi quan hệ giữa hai thực thể; sourceNoteID nil = không gắn
// note nguồn (bảng relations cho phép — purge xử lý qua created_at).
func (s *Store) InsertRelation(ctx context.Context, spaceID int64, fromRef, toRef, relType string, sourceNoteID *int64, now time.Time) (int64, error) {
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO relations(space_id, from_ref, to_ref, rel_type, source_note_id, created_at)
		 VALUES(?,?,?,?,?,?)`,
		spaceID, fromRef, toRef, relType, sourceNoteID, ts(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RecentRelations: quan hệ của space tạo từ since trở đi, mới nhất trước, tối
// đa limit (dedup bộ ba là việc của caller hiển thị).
func (s *Store) RecentRelations(ctx context.Context, spaceID int64, since time.Time, limit int) ([]Relation, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT r.id, r.space_id, r.from_ref, r.to_ref, r.rel_type, r.source_note_id, r.created_at
		 FROM relations r LEFT JOIN notes n ON n.id = r.source_note_id
		 WHERE r.space_id=? AND r.created_at>=? AND (r.source_note_id IS NULL OR n.deleted_at IS NULL)
		 ORDER BY r.created_at DESC LIMIT ?`,
		spaceID, ts(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRelation(sc rowScanner) (Relation, error) {
	var r Relation
	var src sql.NullInt64
	var createdAt string
	if err := sc.Scan(&r.ID, &r.SpaceID, &r.FromRef, &r.ToRef, &r.RelType, &src, &createdAt); err != nil {
		return Relation{}, err
	}
	if src.Valid {
		r.SourceNoteID = &src.Int64
	}
	r.CreatedAt = parseTS(createdAt)
	return r, nil
}

// AllRelationsForExport: mọi quan hệ (mọi space), theo id.
func (s *Store) AllRelationsForExport(ctx context.Context) ([]Relation, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id, space_id, from_ref, to_ref, rel_type, source_note_id, created_at FROM relations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

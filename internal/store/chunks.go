package store

import (
	"context"
)

// Chunk sống trong store (brain map từ brain.Chunk sang để tránh import vòng).
type Chunk struct {
	Ordinal    int
	Text       string
	TokenCount int
}

// InsertChunks ghi chunk của note, INSERT OR IGNORE theo UNIQUE(note_id, ordinal).
func (s *Store) InsertChunks(ctx context.Context, noteID int64, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range chunks {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO chunks(note_id, ordinal, text, token_count) VALUES(?,?,?,?)`,
			noteID, c.Ordinal, c.Text, c.TokenCount); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.BumpGen()
	return nil
}

// ChunksOfNote trả chunk theo ordinal tăng dần.
func (s *Store) ChunksOfNote(ctx context.Context, noteID int64) ([]Chunk, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT ordinal, text, token_count FROM chunks WHERE note_id=? ORDER BY ordinal`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.Ordinal, &c.Text, &c.TokenCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChunkTextsByIDs trả map chunk_id → text bằng một query IN.
func (s *Store) ChunkTextsByIDs(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id, text FROM chunks WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, err
		}
		out[id] = text
	}
	return out, rows.Err()
}

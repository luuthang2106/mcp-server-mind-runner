package store

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"time"
)

// ChunkRef: chunk thiếu embedding.
type ChunkRef struct {
	ID   int64
	Text string
}

// encodeVec/decodeVec: float32 LE, dim*4 bytes (khớp cột BLOB vec).
func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// UpsertEmbedding ghi đè theo (chunk_id, model) — đổi model = backfill.
func (s *Store) UpsertEmbedding(ctx context.Context, chunkID int64, model string, vec []float32, now time.Time) error {
	_, err := s.DB().ExecContext(ctx,
		`INSERT INTO embeddings(chunk_id, model, dim, vec, created_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(chunk_id, model) DO UPDATE SET
		   dim=excluded.dim, vec=excluded.vec, created_at=excluded.created_at`,
		chunkID, model, len(vec), encodeVec(vec), ts(now))
	if err != nil {
		return err
	}
	s.BumpGen()
	return nil
}

// ChunksMissingEmbeddings: chunk của note chưa có embedding theo model, theo ordinal.
func (s *Store) ChunksMissingEmbeddings(ctx context.Context, noteID int64, model string) ([]ChunkRef, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT c.id, c.text FROM chunks c
		 LEFT JOIN embeddings e ON e.chunk_id = c.id AND e.model = ?
		 WHERE c.note_id = ? AND e.chunk_id IS NULL
		 ORDER BY c.ordinal`, model, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkRef
	for rows.Next() {
		var r ChunkRef
		if err := rows.Scan(&r.ID, &r.Text); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MissingEmbedNoteIDs: DISTINCT note thiếu embedding theo model (bỏ note xoá
// mềm); spaceIDs rỗng = mọi space.
func (s *Store) MissingEmbedNoteIDs(ctx context.Context, model string, spaceIDs []int64, limit int) ([]int64, error) {
	q := `SELECT DISTINCT c.note_id FROM chunks c
	      JOIN notes n ON n.id = c.note_id
	      LEFT JOIN embeddings e ON e.chunk_id = c.id AND e.model = ?
	      WHERE e.chunk_id IS NULL AND n.deleted_at IS NULL`
	args := []any{model}
	if len(spaceIDs) > 0 {
		q += ` AND n.space_id IN (` + placeholders(len(spaceIDs)) + `)`
		for _, id := range spaceIDs {
			args = append(args, id)
		}
	}
	q += ` ORDER BY c.note_id LIMIT ?`
	args = append(args, limit)

	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// VecRow: một embedding đọc lên để quét vector, kèm note chứa chunk.
type VecRow struct {
	ChunkID, NoteID int64
	Kind, Status    string // để lọc trong bộ nhớ mà không đổi khoá cache
	Project         string
	Vec             []float32
}

// VectorsForSpaces: mọi embedding theo model của chunk thuộc note khớp filter
// (cùng điều kiện với SearchFTS). ORDER BY c.id để tập kết quả tất định.
func (s *Store) VectorsForSpaces(ctx context.Context, model string, f NoteFilter) ([]VecRow, error) {
	cond, fargs := f.sql()
	q := `SELECT e.chunk_id, c.note_id, n.kind, n.status, COALESCE(n.project, ''), e.vec FROM embeddings e
	      JOIN chunks c ON c.id = e.chunk_id
	      JOIN notes n ON n.id = c.note_id
	      WHERE e.model = ?` + cond
	args := append([]any{model}, fargs...)
	q += ` ORDER BY c.id`

	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VecRow
	for rows.Next() {
		var r VecRow
		var blob []byte
		if err := rows.Scan(&r.ChunkID, &r.NoteID, &r.Kind, &r.Status, &r.Project, &blob); err != nil {
			return nil, err
		}
		r.Vec = decodeVec(blob)
		out = append(out, r)
	}
	return out, rows.Err()
}

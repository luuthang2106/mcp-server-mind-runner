package store

import (
	"context"
	"database/sql"
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

// quantize: chuẩn hoá L2 rồi lượng tử int8 theo scale riêng mỗi vector
// (x ≈ q*scale). Vector 0 → scale 0 (điểm luôn 0).
func quantize(v []float32) (q []byte, scale float32) {
	var n2 float64
	for _, x := range v {
		n2 += float64(x) * float64(x)
	}
	q = make([]byte, len(v))
	if n2 == 0 {
		return q, 0
	}
	inv := 1 / math.Sqrt(n2)
	var maxAbs float64
	for _, x := range v {
		maxAbs = math.Max(maxAbs, math.Abs(float64(x)*inv))
	}
	s := maxAbs / 127
	for i, x := range v {
		q[i] = byte(int8(math.Round(float64(x) * inv / s)))
	}
	return q, float32(s)
}

// UpsertEmbedding ghi đè theo (chunk_id, model) — đổi model = backfill. Lưu
// int8 + scale (xem 0008_vec_int8.sql).
func (s *Store) UpsertEmbedding(ctx context.Context, chunkID int64, model string, vec []float32, now time.Time) error {
	q, scale := quantize(vec)
	_, err := s.DB().ExecContext(ctx,
		`INSERT INTO embeddings(chunk_id, model, dim, vec, scale, created_at) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(chunk_id, model) DO UPDATE SET
		   dim=excluded.dim, vec=excluded.vec, scale=excluded.scale, created_at=excluded.created_at`,
		chunkID, model, len(vec), q, scale, ts(now))
	if err != nil {
		return err
	}
	s.BumpGen()
	return nil
}

// QuantizeLegacyEmbeddings chuyển row float32 (scale NULL) sang int8, từng lô
// 500 row mỗi tx. Trả số row đã chuyển.
func (s *Store) QuantizeLegacyEmbeddings(ctx context.Context) (int, error) {
	total := 0
	for {
		rows, err := s.DB().QueryContext(ctx,
			`SELECT rowid, dim, vec FROM embeddings WHERE scale IS NULL LIMIT 500`)
		if err != nil {
			return total, err
		}
		type item struct {
			rowid int64
			q     []byte
			scale float32
		}
		var items []item
		var bad []int64
		for rows.Next() {
			var id int64
			var dim int
			var blob []byte
			if err := rows.Scan(&id, &dim, &blob); err != nil {
				rows.Close()
				return total, err
			}
			if len(blob) != dim*4 {
				bad = append(bad, id) // hỏng → xoá, backfill embed lại
				continue
			}
			q, sc := quantize(decodeVec(blob))
			items = append(items, item{id, q, sc})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, err
		}
		if len(items)+len(bad) == 0 {
			return total, nil
		}
		tx, err := s.DB().BeginTx(ctx, nil)
		if err != nil {
			return total, err
		}
		for _, it := range items {
			if _, err := tx.ExecContext(ctx, `UPDATE embeddings SET vec=?, scale=? WHERE rowid=? AND scale IS NULL`,
				it.q, it.scale, it.rowid); err != nil {
				tx.Rollback()
				return total, err
			}
		}
		for _, id := range bad {
			if _, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE rowid=?`, id); err != nil {
				tx.Rollback()
				return total, err
			}
		}
		if err := tx.Commit(); err != nil {
			return total, err
		}
		total += len(items)
		s.BumpGen()
	}
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

// VecRow: một embedding khi quét; Raw chỉ hợp lệ trong callback (không giữ lại).
type VecRow struct {
	ChunkID, NoteID int64
	Dim             int
	Scale           float64 // 0 khi Legacy
	Legacy          bool    // float32 chưa chuyển (binary cũ vừa ghi)
	Raw             []byte
}

// Dot: điểm cosine với q ĐÃ chuẩn hoá L2. Sai chiều → (0, false).
func (r *VecRow) Dot(q []float32) (float64, bool) {
	if r.Dim != len(q) {
		return 0, false
	}
	if r.Legacy {
		if len(r.Raw) != 4*r.Dim {
			return 0, false
		}
		var dot, n2 float64
		for i := range q {
			x := float64(math.Float32frombits(binary.LittleEndian.Uint32(r.Raw[i*4:])))
			dot += x * float64(q[i])
			n2 += x * x
		}
		if n2 == 0 {
			return 0, true
		}
		return dot / math.Sqrt(n2), true
	}
	if len(r.Raw) != r.Dim {
		return 0, false
	}
	var dot float32
	raw := r.Raw[:len(q)]
	for i, x := range q {
		dot += float32(int8(raw[i])) * x
	}
	return float64(dot) * r.Scale, true
}

// ScanVectors gọi fn cho mọi embedding theo model của chunk thuộc note khớp
// filter (cùng điều kiện SearchFTS), đọc thẳng từ SQLite — không giữ vector
// trong RAM. Thứ tự không đảm bảo; fn trả lỗi → dừng.
func (s *Store) ScanVectors(ctx context.Context, model string, f NoteFilter, fn func(*VecRow) error) error {
	cond, fargs := f.sql()
	q := `SELECT e.chunk_id, c.note_id, e.dim, e.scale, e.vec FROM embeddings e
	      JOIN chunks c ON c.id = e.chunk_id
	      JOIN notes n ON n.id = c.note_id
	      WHERE e.model = ?` + cond
	args := append([]any{model}, fargs...)
	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var r VecRow
	var scale sql.NullFloat64
	var raw sql.RawBytes
	for rows.Next() {
		if err := rows.Scan(&r.ChunkID, &r.NoteID, &r.Dim, &scale, &raw); err != nil {
			return err
		}
		r.Legacy, r.Scale, r.Raw = !scale.Valid, scale.Float64, raw
		if err := fn(&r); err != nil {
			return err
		}
	}
	return rows.Err()
}

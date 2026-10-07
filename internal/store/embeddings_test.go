package store

import (
	"context"
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// TestEmbeddingRoundtripAndUpsert: int8 dim bytes + scale, dequantize ≈ vector
// chuẩn hoá;
// upsert cùng (chunk, model) đè vec + created_at, không nhân hàng.
func TestEmbeddingRoundtripAndUpsert(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	noteID, _, err := s.UpsertNote(ctx, noteAt(1, "note", "văn bản", t0))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertChunks(ctx, noteID, []Chunk{{Ordinal: 0, Text: "một", TokenCount: 1}}); err != nil {
		t.Fatal(err)
	}
	refs, err := s.ChunksMissingEmbeddings(ctx, noteID, "m1")
	if err != nil || len(refs) != 1 || refs[0].Text != "một" {
		t.Fatalf("refs=%+v err=%v", refs, err)
	}

	vec := []float32{0.5, -1.25, 3}
	if err := s.UpsertEmbedding(ctx, refs[0].ID, "m1", vec, t0); err != nil {
		t.Fatal(err)
	}
	var dim, blen int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT dim, length(vec) FROM embeddings WHERE chunk_id=? AND model='m1'`, refs[0].ID).Scan(&dim, &blen); err != nil {
		t.Fatal(err)
	}
	if dim != 3 || blen != 3 {
		t.Fatalf("dim=%d blen=%d, muốn 3/3 (int8)", dim, blen)
	}
	assertStoredCos(t, s, refs[0].ID, vec)

	// upsert đè cùng (chunk, model)
	newVec := []float32{9, 8, 7, 6}
	t1 := t0.Add(time.Hour)
	if err := s.UpsertEmbedding(ctx, refs[0].ID, "m1", newVec, t1); err != nil {
		t.Fatal(err)
	}
	var n int
	var createdAt string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(created_at),'') FROM embeddings WHERE chunk_id=? AND model='m1'`, refs[0].ID).Scan(&n, &createdAt); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows=%d, muốn 1", n)
	}
	if createdAt != ts(t1) {
		t.Fatalf("created_at=%s, muốn %s", createdAt, ts(t1))
	}
	assertStoredCos(t, s, refs[0].ID, newVec)
}

// assertStoredCos: vector lưu (int8) có cosine ≈ 1 với vector gốc.
func assertStoredCos(t *testing.T, s *Store, chunkID int64, want []float32) {
	t.Helper()
	var scale float64
	var raw []byte
	if err := s.DB().QueryRowContext(context.Background(),
		`SELECT scale, vec FROM embeddings WHERE chunk_id=? AND model='m1'`, chunkID).Scan(&scale, &raw); err != nil {
		t.Fatal(err)
	}
	r := VecRow{Dim: len(want), Scale: scale, Raw: raw}
	got, ok := r.Dot(unit(want))
	if !ok || math.Abs(got-1) > 0.01 {
		t.Fatalf("cos(lưu, gốc)=%v ok=%v, muốn ≈1", got, ok)
	}
}

func unit(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / math.Sqrt(n))
	}
	return out
}

// TestQuantizeQuality: vector 1024 chiều ngẫu nhiên — cosine giữa bản int8 và
// vector khác khớp float64 trong ±0.01.
func TestQuantizeQuality(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for k := 0; k < 20; k++ {
		a, b := make([]float32, 1024), make([]float32, 1024)
		for i := range a {
			a[i], b[i] = float32(r.NormFloat64()), float32(r.NormFloat64())+a[i]*0.5
		}
		q, sc := quantize(a)
		row := VecRow{Dim: 1024, Scale: float64(sc), Raw: q}
		got, _ := row.Dot(unit(b))
		var dot float64
		ua, ub := unit(a), unit(b)
		for i := range ua {
			dot += float64(ua[i]) * float64(ub[i])
		}
		if math.Abs(got-dot) > 0.01 {
			t.Fatalf("int8 cos=%v, float cos=%v", got, dot)
		}
	}
}

// TestQuantizeLegacy: row float32 cũ (scale NULL, như binary cũ ghi) được quét
// đúng, rồi QuantizeLegacyEmbeddings chuyển sang int8; row hỏng bị xoá.
func TestQuantizeLegacy(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	noteID, _, err := s.UpsertNote(ctx, noteAt(1, "note", "cũ", t0))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertChunks(ctx, noteID, []Chunk{{Ordinal: 0, Text: "a", TokenCount: 1}, {Ordinal: 1, Text: "b", TokenCount: 1}}); err != nil {
		t.Fatal(err)
	}
	refs, _ := s.ChunksMissingEmbeddings(ctx, noteID, "m1")
	vec := []float32{3, 4}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO embeddings(chunk_id, model, dim, vec, created_at) VALUES(?,?,?,?,?),(?,?,?,?,?)`,
		refs[0].ID, "m1", 2, encodeVec(vec), ts(t0), refs[1].ID, "m1", 3, []byte{1, 2}, ts(t0)); err != nil {
		t.Fatal(err)
	}
	var scores []float64
	if err := s.ScanVectors(ctx, "m1", NoteFilter{}, func(r *VecRow) error {
		if sc, ok := r.Dot([]float32{0.6, 0.8}); ok {
			scores = append(scores, sc)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(scores) != 1 || math.Abs(scores[0]-1) > 1e-6 {
		t.Fatalf("legacy scores=%v, muốn [1]", scores)
	}
	n, err := s.QuantizeLegacyEmbeddings(ctx)
	if err != nil || n != 1 {
		t.Fatalf("quantized=%d err=%v, muốn 1", n, err)
	}
	var cnt, legacy int
	s.DB().QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*)-COUNT(scale) FROM embeddings`).Scan(&cnt, &legacy)
	if cnt != 1 || legacy != 0 {
		t.Fatalf("rows=%d legacy=%d, muốn 1/0 (row hỏng xoá)", cnt, legacy)
	}
	assertStoredCos(t, s, refs[0].ID, vec)
	if n, _ := s.QuantizeLegacyEmbeddings(ctx); n != 0 {
		t.Fatalf("lần 2 quantized=%d, muốn 0", n)
	}
}

// TestChunksMissingEmbeddings: đúng chunk thiếu theo model, thứ tự ordinal.
func TestChunksMissingEmbeddings(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	noteID, _, err := s.UpsertNote(ctx, noteAt(1, "note", "văn bản", t0))
	if err != nil {
		t.Fatal(err)
	}
	chunks := []Chunk{
		{Ordinal: 0, Text: "không", TokenCount: 1},
		{Ordinal: 1, Text: "một", TokenCount: 1},
		{Ordinal: 2, Text: "hai", TokenCount: 1},
	}
	if err := s.InsertChunks(ctx, noteID, chunks); err != nil {
		t.Fatal(err)
	}
	refs, err := s.ChunksMissingEmbeddings(ctx, noteID, "m1")
	if err != nil || len(refs) != 3 {
		t.Fatalf("refs=%d err=%v", len(refs), err)
	}
	// embed chunk ordinal 0 (refs theo ordinal)
	if err := s.UpsertEmbedding(ctx, refs[0].ID, "m1", []float32{1}, t0); err != nil {
		t.Fatal(err)
	}
	missing, err := s.ChunksMissingEmbeddings(ctx, noteID, "m1")
	if err != nil || len(missing) != 2 || missing[0].Text != "một" || missing[1].Text != "hai" {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	// model khác → cả 3 thiếu (embedding theo model, không dùng chung)
	missing, err = s.ChunksMissingEmbeddings(ctx, noteID, "m2")
	if err != nil || len(missing) != 3 {
		t.Fatalf("m2 missing=%d err=%v", len(missing), err)
	}
}

// TestScanVectors: join chunks+notes (bỏ note xoá mềm), lọc model/space/tag.
func TestScanVectors(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	seedNote := func(spaceID int64, text string) (noteID, chunkID int64) {
		t.Helper()
		id, _, err := s.UpsertNote(ctx, noteAt(spaceID, "note", text, t0))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.InsertChunks(ctx, id, []Chunk{{Ordinal: 0, Text: text, TokenCount: 1}}); err != nil {
			t.Fatal(err)
		}
		refs, err := s.ChunksMissingEmbeddings(ctx, id, "m1")
		if err != nil || len(refs) != 1 {
			t.Fatalf("refs=%+v err=%v", refs, err)
		}
		return id, refs[0].ID
	}

	_, c1 := seedNote(1, "một")
	_, c2 := seedNote(1, "hai")
	_, c3 := seedNote(2, "ba")
	_, c4 := seedNote(1, "bốn xoá")
	for i, c := range []int64{c1, c2, c3, c4} {
		if err := s.UpsertEmbedding(ctx, c, "m1", []float32{float32(i + 1)}, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertEmbedding(ctx, c1, "m2", []float32{9, 9}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET deleted_at=? WHERE id=(SELECT note_id FROM chunks WHERE id=?)`, ts(t0), c4); err != nil {
		t.Fatal(err)
	}

	type row struct {
		ChunkID int64
		Dim     int
	}
	scan := func(model string, f NoteFilter) []row {
		t.Helper()
		var out []row
		if err := s.ScanVectors(ctx, model, f, func(r *VecRow) error {
			out = append(out, row{r.ChunkID, r.Dim})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		slices.SortFunc(out, func(a, b row) int { return int(a.ChunkID - b.ChunkID) })
		return out
	}
	rows := scan("m1", NoteFilter{})
	if len(rows) != 3 || rows[0].ChunkID != c1 || rows[1].ChunkID != c2 || rows[2].ChunkID != c3 {
		t.Fatalf("rows=%+v, muốn [%d %d %d] (bỏ note xoá)", rows, c1, c2, c3)
	}

	if rows = scan("m1", NoteFilter{SpaceIDs: []int64{1}}); len(rows) != 2 {
		t.Fatalf("space1 rows=%d", len(rows))
	}
	if rows = scan("m2", NoteFilter{}); len(rows) != 1 || rows[0].Dim != 2 {
		t.Fatalf("m2 rows=%+v", rows)
	}

	// lọc tags giống SearchFTS: chỉ note mang đủ tag
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET tags='["work","x"]' WHERE id=(SELECT note_id FROM chunks WHERE id=?)`, c2); err != nil {
		t.Fatal(err)
	}
	if rows = scan("m1", NoteFilter{Tags: []string{"work"}}); len(rows) != 1 || rows[0].ChunkID != c2 {
		t.Fatalf("tag rows=%+v", rows)
	}
	if rows = scan("m1", NoteFilter{Tags: []string{"work", "khác"}}); len(rows) != 0 {
		t.Fatalf("2 tag rows=%+v", rows)
	}
}

// TestMissingEmbedNoteIDs: note thiếu embedding (theo model), lọc space,
// bỏ note đã xoá, tôn trọng limit.
func TestMissingEmbedNoteIDs(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	newNote := func(spaceID int64, text string) int64 {
		t.Helper()
		id, _, err := s.UpsertNote(ctx, noteAt(spaceID, "note", text, t0))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.InsertChunks(ctx, id, []Chunk{{Ordinal: 0, Text: text, TokenCount: 1}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	embedAll := func(noteID int64) {
		t.Helper()
		refs, err := s.ChunksMissingEmbeddings(ctx, noteID, "m1")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range refs {
			if err := s.UpsertEmbedding(ctx, r.ID, "m1", []float32{1}, t0); err != nil {
				t.Fatal(err)
			}
		}
	}

	idA := newNote(1, "A thiếu")  // space personal
	idB := newNote(1, "B đủ")     // đủ embedding
	idC := newNote(2, "C thiếu")  // space work
	idD := newNote(1, "D đã xoá") // xoá mềm → loại
	embedAll(idB)
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET deleted_at=? WHERE id=?`, ts(t0), idD); err != nil {
		t.Fatal(err)
	}

	ids, err := s.MissingEmbedNoteIDs(ctx, "m1", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []int64{idA, idC}) {
		t.Fatalf("ids=%v, muốn [%d %d]", ids, idA, idC)
	}
	// lọc theo space
	ids, err = s.MissingEmbedNoteIDs(ctx, "m1", []int64{1}, 100)
	if err != nil || !slices.Equal(ids, []int64{idA}) {
		t.Fatalf("space1 ids=%v err=%v", ids, err)
	}
	// limit
	ids, err = s.MissingEmbedNoteIDs(ctx, "m1", nil, 1)
	if err != nil || !slices.Equal(ids, []int64{idA}) {
		t.Fatalf("limit ids=%v err=%v", ids, err)
	}
	// model chưa embed ai → cả A, B, C
	ids, err = s.MissingEmbedNoteIDs(ctx, "m2", nil, 100)
	if err != nil || !slices.Equal(ids, []int64{idA, idB, idC}) {
		t.Fatalf("m2 ids=%v err=%v", ids, err)
	}
}

package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestEmbeddingRoundtripAndUpsert: float32 LE dim*4 bytes, decode khớp;
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
	if dim != 3 || blen != 12 {
		t.Fatalf("dim=%d blen=%d, muốn 3/12 (float32 LE)", dim, blen)
	}
	var raw []byte
	if err := s.DB().QueryRowContext(ctx,
		`SELECT vec FROM embeddings WHERE chunk_id=? AND model='m1'`, refs[0].ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := decodeVec(raw); !slices.Equal(got, vec) {
		t.Fatalf("decode=%v, muốn %v", got, vec)
	}

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
	if err := s.DB().QueryRowContext(ctx,
		`SELECT vec FROM embeddings WHERE chunk_id=? AND model='m1'`, refs[0].ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got := decodeVec(raw); !slices.Equal(got, newVec) {
		t.Fatalf("decode sau upsert=%v, muốn %v", got, newVec)
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

// TestVectorsForSpaces: join chunks+notes (bỏ note xoá mềm), lọc model/space,
// thứ tự chunk_id, vec roundtrip.
func TestVectorsForSpaces(t *testing.T) {
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

	rows, err := s.VectorsForSpaces(ctx, "m1", NoteFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d, muốn 3 (bỏ note xoá)", len(rows))
	}
	for i, want := range []int64{c1, c2, c3} {
		if rows[i].ChunkID != want {
			t.Fatalf("rows[%d].ChunkID=%d, muốn %d (order chunk_id)", i, rows[i].ChunkID, want)
		}
	}
	if got := rows[0].Vec; len(got) != 1 || got[0] != 1 {
		t.Fatalf("vec roundtrip=%v", got)
	}

	rows, err = s.VectorsForSpaces(ctx, "m1", NoteFilter{SpaceIDs: []int64{1}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("space1 rows=%d err=%v", len(rows), err)
	}
	rows, err = s.VectorsForSpaces(ctx, "m2", NoteFilter{})
	if err != nil || len(rows) != 1 || len(rows[0].Vec) != 2 {
		t.Fatalf("m2 rows=%+v err=%v", rows, err)
	}

	// lọc tags giống SearchFTS: chỉ note mang đủ tag
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET tags='["work","x"]' WHERE id=(SELECT note_id FROM chunks WHERE id=?)`, c2); err != nil {
		t.Fatal(err)
	}
	rows, err = s.VectorsForSpaces(ctx, "m1", NoteFilter{Tags: []string{"work"}})
	if err != nil || len(rows) != 1 || rows[0].ChunkID != c2 {
		t.Fatalf("tag rows=%+v err=%v", rows, err)
	}
	rows, err = s.VectorsForSpaces(ctx, "m1", NoteFilter{Tags: []string{"work", "khác"}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("2 tag rows=%+v err=%v", rows, err)
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

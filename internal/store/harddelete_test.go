package store

import (
	"context"
	"testing"
	"time"
)

// TestHardDeleteNotes: xoá cứng đủ chuỗi (relations, chunks, embeddings,
// chunks_fts, notes) trong một tx; note ngoài danh sách không bị đụng; chạy
// lại trên id đã xoá → 0, không lỗi.
func TestHardDeleteNotes(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	kill, _, err := s.UpsertNote(ctx, noteAt(1, "decision", "quyết định cũ", t0))
	if err != nil {
		t.Fatal(err)
	}
	keep, _, err := s.UpsertNote(ctx, noteAt(1, "decision", "quyết định mới", t0))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{kill, keep} {
		if err := s.InsertChunks(ctx, id, []Chunk{{Ordinal: 0, Text: "chunk", TokenCount: 1}}); err != nil {
			t.Fatal(err)
		}
		var chunkID int64
		if err := s.DB().QueryRowContext(ctx, `SELECT id FROM chunks WHERE note_id=?`, id).Scan(&chunkID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO embeddings(chunk_id, model, dim, vec, created_at) VALUES(?,?,?,?,?)`,
			chunkID, "m", 2, make([]byte, 8), ts(t0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.InsertRelation(ctx, 1, "A", "B", "related", &kill, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRelation(ctx, 1, "C", "D", "related", &keep, t0); err != nil {
		t.Fatal(err)
	}

	n, err := s.HardDeleteNotes(ctx, []int64{kill})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := s.FetchNote(ctx, kill); err == nil {
		t.Fatal("note chưa bị xoá")
	}
	counts := map[string]int{}
	for k, q := range map[string]string{
		"chunks":     `SELECT COUNT(*) FROM chunks`,
		"embeddings": `SELECT COUNT(*) FROM embeddings`,
		"fts":        `SELECT COUNT(*) FROM chunks_fts`,
		"relations":  `SELECT COUNT(*) FROM relations`,
	} {
		var cnt int
		if err := s.DB().QueryRowContext(ctx, q).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		counts[k] = cnt
	}
	if counts["chunks"] != 1 || counts["embeddings"] != 1 || counts["fts"] != 1 || counts["relations"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	if _, err := s.FetchNote(ctx, keep); err != nil {
		t.Fatalf("note giữ bị đụng: %v", err)
	}
	n2, err := s.HardDeleteNotes(ctx, []int64{kill})
	if err != nil || n2 != 0 {
		t.Fatalf("chạy lại: n=%d err=%v", n2, err)
	}
}

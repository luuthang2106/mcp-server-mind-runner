package store

import (
	"context"
	"testing"
	"time"
)

func noteAt(spaceID int64, kind, text string, at time.Time) *Note {
	return &Note{SpaceID: spaceID, Kind: kind, Text: text, Source: "tool:remember", CreatedAt: at, UpdatedAt: at}
}

func TestUpsertNoteIdempotent(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id, fresh, err := s.UpsertNote(ctx, noteAt(1, "note", "xin chào", t0))
	if err != nil || !fresh || id <= 0 {
		t.Fatalf("id=%d fresh=%v err=%v", id, fresh, err)
	}

	// lần 2 cùng space/kind/text → "ôn lại": cùng id, fresh=false, updated_at mới
	t1 := t0.Add(time.Hour)
	id2, fresh2, err := s.UpsertNote(ctx, noteAt(1, "note", "xin chào", t1))
	if err != nil || fresh2 || id2 != id {
		t.Fatalf("id2=%d fresh2=%v err=%v", id2, fresh2, err)
	}
	n, err := s.FetchNote(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !n.UpdatedAt.Equal(t1) {
		t.Fatalf("updated_at=%v, muốn %v", n.UpdatedAt, t1)
	}

	// đã xoá mem → upsert lại hồi sinh (deleted_at=NULL)
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET deleted_at=? WHERE id=?`, ts(t1), id); err != nil {
		t.Fatal(err)
	}
	t2 := t0.Add(2 * time.Hour)
	if _, _, err := s.UpsertNote(ctx, noteAt(1, "note", "xin chào", t2)); err != nil {
		t.Fatal(err)
	}
	n, err = s.FetchNote(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n.DeletedAt != nil {
		t.Fatalf("deleted_at=%v, muốn nil", n.DeletedAt)
	}

	// khác kind → note mới
	id3, fresh3, err := s.UpsertNote(ctx, noteAt(1, "fact", "xin chào", t1))
	if err != nil || !fresh3 || id3 == id {
		t.Fatalf("id3=%d fresh3=%v err=%v", id3, fresh3, err)
	}
}

func TestFetchNoteRoundtrip(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	n := noteAt(1, "preference", "thích cà phê", t0)
	n.Tags = []string{"đồ uống", "sở thích"}
	id, _, err := s.UpsertNote(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.FetchNote(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "preference" || got.Text != "thích cà phê" || got.Source != "tool:remember" {
		t.Fatalf("got=%+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "đồ uống" {
		t.Fatalf("tags=%v", got.Tags)
	}
	if got.ContentHash == "" || got.DeletedAt != nil || got.SessionID != nil {
		t.Fatalf("got=%+v", got)
	}
}

func TestSpaceByName(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	id, err := s.SpaceByName(ctx, "personal")
	if err != nil || id <= 0 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if _, err := s.SpaceByName(ctx, "không-có"); err == nil {
		t.Fatal("space lạ phải lỗi")
	}
}

// TestSoftDeleteNote: xoá mềm → ẩn khỏi NotesByKind/AllNotesForExport,
// FetchNote còn nguyên + DeletedAt set; gọi lần 2 / id sai → false.
func TestSoftDeleteNote(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id, _, err := s.UpsertNote(ctx, noteAt(1, "note", "sắp quên", t0))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SoftDeleteNote(ctx, id, t0); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	n, err := s.FetchNote(ctx, id)
	if err != nil || n.DeletedAt == nil {
		t.Fatalf("n=%+v err=%v", n, err)
	}
	got, err := s.NotesByKind(ctx, 1, "note", time.Time{}, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("NotesByKind=%+v err=%v", got, err)
	}
	all, err := s.AllNotesForExport(ctx)
	if err != nil || len(all) != 0 {
		t.Fatalf("AllNotesForExport=%+v err=%v", all, err)
	}
	if ok, err := s.SoftDeleteNote(ctx, id, t0); err != nil || ok {
		t.Fatalf("lần 2: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SoftDeleteNote(ctx, 9999, t0); err != nil || ok {
		t.Fatalf("id sai: ok=%v err=%v", ok, err)
	}

	// AllNotesForExport: chỉ note sống, theo id
	id2, _, err := s.UpsertNote(ctx, noteAt(2, "fact", "còn sống", t0))
	if err != nil {
		t.Fatal(err)
	}
	all, err = s.AllNotesForExport(ctx)
	if err != nil || len(all) != 1 || all[0].ID != id2 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestInsertChunksIdempotent(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id, _, err := s.UpsertNote(ctx, noteAt(1, "note", "văn bản", t0))
	if err != nil {
		t.Fatal(err)
	}
	chunks := []Chunk{{Ordinal: 0, Text: "phần một", TokenCount: 2}, {Ordinal: 1, Text: "phần hai", TokenCount: 2}}
	if err := s.InsertChunks(ctx, id, chunks); err != nil {
		t.Fatal(err)
	}
	// ghi lặp → OR IGNORE, không lỗi, không nhân bản
	if err := s.InsertChunks(ctx, id, chunks); err != nil {
		t.Fatal(err)
	}
	got, err := s.ChunksOfNote(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Ordinal != 0 || got[0].Text != "phần một" || got[0].TokenCount != 2 || got[1].Ordinal != 1 {
		t.Fatalf("got=%+v", got)
	}
}

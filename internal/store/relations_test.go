package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestInsertRelationWithAndWithoutSourceNote: source_note_id set khi có note
// nguồn, NULL khi không — cả hai đọc lại đúng giá trị.
func TestInsertRelationWithAndWithoutSourceNote(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	noteID, _, err := st.UpsertNote(ctx, &Note{
		SpaceID: 1, Kind: "fact", Text: "SQLite local-first", Source: "test",
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	relID, err := st.InsertRelation(ctx, 1, "Viết tài liệu", "SQLite local-first", "mentions", &noteID, now)
	if err != nil || relID <= 0 {
		t.Fatalf("relID=%d err=%v", relID, err)
	}
	relID2, err := st.InsertRelation(ctx, 1, "a", "b", "related", nil, now)
	if err != nil || relID2 <= 0 || relID2 == relID {
		t.Fatalf("relID2=%d err=%v", relID2, err)
	}

	var from, to, rtype string
	var src sql.NullInt64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT from_ref, to_ref, rel_type, source_note_id FROM relations WHERE id=?`, relID).
		Scan(&from, &to, &rtype, &src); err != nil {
		t.Fatal(err)
	}
	if from != "Viết tài liệu" || to != "SQLite local-first" || rtype != "mentions" || !src.Valid || src.Int64 != noteID {
		t.Fatalf("row1: %q %q %q src=%+v", from, to, rtype, src)
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT source_note_id FROM relations WHERE id=?`, relID2).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src.Valid {
		t.Fatalf("row2 source_note_id=%+v, muốn NULL", src)
	}
}

// TestRecentRelations: lọc space + since, sort created_at DESC, limit;
// source_note_id NULL/giá trị đọc đúng.
func TestRecentRelations(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	noteID, _, err := st.UpsertNote(ctx, &Note{
		SpaceID: 1, Kind: "fact", Text: "x", Source: "test", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	since := now.AddDate(0, 0, -30)
	if _, err := st.InsertRelation(ctx, 1, "a", "b", "related", &noteID, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertRelation(ctx, 1, "c", "d", "mentions", nil, now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertRelation(ctx, 1, "old", "x", "related", nil, now.AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertRelation(ctx, 2, "other", "y", "related", nil, now); err != nil {
		t.Fatal(err)
	}

	rels, err := st.RecentRelations(ctx, 1, since, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Fatalf("rels=%+v", rels)
	}
	if rels[0].FromRef != "c" || rels[1].FromRef != "a" {
		t.Fatalf("order=%+v", rels)
	}
	if rels[1].SourceNoteID == nil || *rels[1].SourceNoteID != noteID {
		t.Fatalf("src=%+v", rels[1])
	}
	if rels[0].SourceNoteID != nil {
		t.Fatalf("src phải NULL: %+v", rels[0])
	}
	if !rels[0].CreatedAt.Equal(now.Add(-1 * time.Hour)) {
		t.Fatalf("at=%v", rels[0].CreatedAt)
	}

	lim, err := st.RecentRelations(ctx, 1, since, 1)
	if err != nil || len(lim) != 1 || lim[0].FromRef != "c" {
		t.Fatalf("lim=%+v err=%v", lim, err)
	}
}

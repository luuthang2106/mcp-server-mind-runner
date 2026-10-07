package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func sp(s string) *string { return &s }

// TestInsertTaskWithFields: trường mới được ghi; trùng title open → chỉ lấp
// trường còn trống, không ghi đè trường đã có; UpdateTaskWith "" = xoá.
func TestInsertTaskWithFields(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id, created, err := st.InsertTaskWith(ctx, 1, "Gửi báo giá", TaskFields{
		Why: sp("khách cần trước thứ sáu"), Owner: sp("Lan"), DueAt: sp("2026-10-09"),
	}, now)
	if err != nil || !created {
		t.Fatalf("id=%d created=%v err=%v", id, created, err)
	}
	id2, created2, err := st.InsertTaskWith(ctx, 1, "Gửi báo giá", TaskFields{
		Owner: sp("Hùng"), WaitingOn: sp("anh Minh"),
	}, now)
	if err != nil || created2 || id2 != id {
		t.Fatalf("dedupe: id=%d created=%v err=%v", id2, created2, err)
	}
	tk, err := st.TaskByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Owner != "Lan" || tk.WaitingOn != "anh Minh" || tk.Why != "khách cần trước thứ sáu" || tk.DueAt != "2026-10-09" {
		t.Fatalf("task=%+v", tk)
	}

	tk, err = st.UpdateTaskWith(ctx, id, nil, TaskFields{WaitingOn: sp(""), DueAt: sp("2026-10-12")}, now.Add(time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if tk.WaitingOn != "" || tk.DueAt != "2026-10-12" || tk.Owner != "Lan" {
		t.Fatalf("sau update: %+v", tk)
	}
}

// TestDueAndWaitingTasks: chỉ task open; due_at <= mốc, sắp theo hạn.
func TestDueAndWaitingTasks(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	mk := func(title string, f TaskFields) int64 {
		id, _, err := st.InsertTaskWith(ctx, 1, title, f, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	late := mk("Trễ", TaskFields{DueAt: sp("2026-10-01")})
	soon := mk("Sắp", TaskFields{DueAt: sp("2026-10-08")})
	mk("Xa", TaskFields{DueAt: sp("2026-12-01")})
	wait := mk("Chờ", TaskFields{WaitingOn: sp("kế toán")})
	done := mk("Xong rồi", TaskFields{DueAt: sp("2026-10-02"), WaitingOn: sp("ai đó")})
	if _, err := st.UpdateTaskWith(ctx, done, sp("done"), TaskFields{}, now, true); err != nil {
		t.Fatal(err)
	}

	due, err := st.DueTasks(ctx, 1, "2026-10-09", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 || due[0].ID != late || due[1].ID != soon {
		t.Fatalf("due=%+v", due)
	}
	ws, err := st.WaitingTasks(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].ID != wait {
		t.Fatalf("waiting=%+v", ws)
	}
}

// TestNoteMetaMergeAndSupersede: ghi lại cùng text kèm meta mới → merge, không
// trùng; supersede ẩn note cũ khỏi NotesByKind; khác space → ErrNoteNotFound.
func TestNoteMetaMergeAndSupersede(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	old := &Note{SpaceID: 1, Kind: "decision", Text: "Dùng Postgres", Source: "mcp", Tags: []string{"db"},
		Meta: NoteMeta{Why: "team quen"}, CreatedAt: now, UpdatedAt: now}
	oldID, _, err := st.UpsertNote(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	again := &Note{SpaceID: 1, Kind: "decision", Text: "Dùng Postgres", Source: "mcp", Tags: []string{"infra"},
		Meta: NoteMeta{Alternatives: []string{"MySQL"}}, CreatedAt: now, UpdatedAt: now.Add(time.Minute)}
	againID, fresh, err := st.UpsertNote(ctx, again)
	if err != nil || fresh || againID != oldID {
		t.Fatalf("merge: id=%d fresh=%v err=%v", againID, fresh, err)
	}
	n, err := st.FetchNote(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Meta.Why != "team quen" || len(n.Meta.Alternatives) != 1 || len(n.Tags) != 2 || n.Status != "active" {
		t.Fatalf("sau merge: meta=%+v tags=%v status=%q", n.Meta, n.Tags, n.Status)
	}

	newID, _, err := st.UpsertNote(ctx, &Note{SpaceID: 1, Kind: "decision", Text: "Chuyển sang SQLite",
		Source: "mcp", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeNote(ctx, oldID, newID, now); err != nil {
		t.Fatal(err)
	}
	ds, err := st.NotesByKind(ctx, 1, "decision", time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].ID != newID {
		t.Fatalf("NotesByKind phải bỏ note superseded: %+v", ds)
	}
	n, _ = st.FetchNote(ctx, oldID)
	if n.Status != "superseded" || n.SupersededBy == nil || *n.SupersededBy != newID {
		t.Fatalf("old=%+v", n)
	}

	otherID, _, err := st.UpsertNote(ctx, &Note{SpaceID: 2, Kind: "decision", Text: "khác space",
		Source: "mcp", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeNote(ctx, newID, otherID, now); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("khác space: err=%v", err)
	}
}

package store

import (
	"context"
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

// TestNoteMetaMergeAndRevive: ghi lại cùng text kèm meta mới → merge, không
// trùng; row legacy status='superseded' ghi lại y hệt → hồi sinh active (nội
// dung không mất trong cửa sổ trước khi purge quét).
func TestNoteMetaMergeAndRevive(t *testing.T) {
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

	// legacy: đánh dấu superseded bằng SQL tay (cơ chế cũ đã bỏ) rồi ghi lại
	// y hệt → hồi sinh active, không mất nội dung.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE notes SET status='superseded', superseded_by=? WHERE id=?`, oldID, oldID); err != nil {
		t.Fatal(err)
	}
	gID, fresh, err := st.UpsertNote(ctx, again)
	if err != nil || fresh || gID != oldID {
		t.Fatalf("hồi sinh: id=%d fresh=%v err=%v", gID, fresh, err)
	}
	n, err = st.FetchNote(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != "active" || n.SupersededBy != nil {
		t.Fatalf("hồi sinh: status=%q superseded_by=%v", n.Status, n.SupersededBy)
	}
}

// TestTaskDedupePerProject: cùng tiêu đề ở hai project là hai việc; cùng
// project thì gộp.
func TestTaskDedupePerProject(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	sp, _ := st.SpaceByName(ctx, "personal")
	now := time.Now()
	a, c1, _ := st.InsertTaskWith(ctx, sp, "Viết README", TaskFields{Project: "api"}, now)
	b, c2, _ := st.InsertTaskWith(ctx, sp, "viết readme", TaskFields{Project: "web"}, now)
	c, c3, _ := st.InsertTaskWith(ctx, sp, "Viết README.", TaskFields{Project: "api"}, now)
	if !c1 || !c2 || c3 || a == b || c != a {
		t.Fatalf("a=%d b=%d c=%d created=%v %v %v", a, b, c, c1, c2, c3)
	}
	ts, err := st.QueryTasks(ctx, sp, TaskQuery{Status: "open", Project: "web", Limit: 10})
	if err != nil || len(ts) != 1 || ts[0].ID != b || ts[0].Project != "web" {
		t.Fatalf("lọc project: %+v err=%v", ts, err)
	}
}

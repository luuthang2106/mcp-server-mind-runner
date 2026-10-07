package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestInsertTaskDedupOpenTitle: task open cùng space cùng title → không tạo
// trùng (chống lặp khi job extract retry); khác space hoặc task cũ đã done → tạo mới.
func TestInsertTaskDedupOpenTitle(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	next := "viết phần 2"

	id1, created, err := st.InsertTask(ctx, 1, "Nộp báo cáo quý", &next, now)
	if err != nil || !created || id1 <= 0 {
		t.Fatalf("id=%d created=%v err=%v", id1, created, err)
	}

	id2, created2, err := st.InsertTask(ctx, 1, "Nộp báo cáo quý", nil, now)
	if err != nil || created2 || id2 != id1 {
		t.Fatalf("lần 2: id=%d created=%v err=%v, muốn id=%d created=false", id2, created2, err, id1)
	}

	id3, created3, err := st.InsertTask(ctx, 2, "Nộp báo cáo quý", nil, now)
	if err != nil || !created3 || id3 == id1 {
		t.Fatalf("space khác: id=%d created=%v err=%v", id3, created3, err)
	}

	var title, status string
	var ns sql.NullString
	if err := st.DB().QueryRowContext(ctx,
		`SELECT title, status, next_step FROM tasks WHERE id=?`, id1).Scan(&title, &status, &ns); err != nil {
		t.Fatal(err)
	}
	if title != "Nộp báo cáo quý" || status != "open" || !ns.Valid || ns.String != "viết phần 2" {
		t.Fatalf("row: title=%q status=%q next_step=%+v", title, status, ns)
	}

	if _, err := st.DB().ExecContext(ctx, `UPDATE tasks SET status='done' WHERE id=?`, id1); err != nil {
		t.Fatal(err)
	}
	id4, created4, err := st.InsertTask(ctx, 1, "Nộp báo cáo quý", nil, now)
	if err != nil || !created4 || id4 == id1 {
		t.Fatalf("sau done: id=%d created=%v err=%v", id4, created4, err)
	}
}

// TestUpdateTaskListAndByID: update status/next_step từng phần, no-op giữ
// nguyên updated_at; ListTasks lọc theo status ("" = tất cả) + space, sort
// updated_at DESC; TaskByID/UpdateTask id sai → error.
func TestUpdateTaskListAndByID(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	idA, _, err := st.InsertTask(ctx, 1, "việc A", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	idB, _, err := st.InsertTask(ctx, 1, "việc B", nil, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	// update status done cho A
	done := "done"
	task, err := st.UpdateTask(ctx, idA, &done, nil, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" || task.Title != "việc A" {
		t.Fatalf("task=%+v", task)
	}
	if !task.UpdatedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("updated_at=%v", task.UpdatedAt)
	}

	// lọc open → chỉ B
	open, err := st.ListTasks(ctx, 1, "open", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != idB {
		t.Fatalf("open=%+v", open)
	}

	// "" → tất cả, updated_at DESC: A (mới sửa) trước B
	all, err := st.ListTasks(ctx, 1, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != idA || all[1].ID != idB {
		t.Fatalf("all=%+v", all)
	}

	// update next_step đơn lẻ → status giữ nguyên
	next := "viết phần 2"
	task, err = st.UpdateTask(ctx, idA, nil, &next, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" || task.NextStep == nil || *task.NextStep != "viết phần 2" {
		t.Fatalf("task=%+v", task)
	}

	// no-op: không field nào → giữ nguyên updated_at
	task, err = st.UpdateTask(ctx, idA, nil, nil, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !task.UpdatedAt.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("no-op đổi updated_at: %v", task.UpdatedAt)
	}

	byID, err := st.TaskByID(ctx, idB)
	if err != nil || byID.Title != "việc B" {
		t.Fatalf("byID=%+v err=%v", byID, err)
	}
	other, err := st.ListTasks(ctx, 2, "open", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("space 2=%+v", other)
	}

	if _, err := st.TaskByID(ctx, 9999); err == nil {
		t.Fatal("TaskByID id sai phải error")
	}
	if _, err := st.UpdateTask(ctx, 9999, &done, nil, now); err == nil {
		t.Fatal("UpdateTask id sai phải error")
	}
}

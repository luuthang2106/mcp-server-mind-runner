package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Task một dòng bảng tasks. Các trường 5W rỗng = người dùng chưa nói (không bịa).
type Task struct {
	ID          int64
	SpaceID     int64
	Title       string // What
	Status      string
	NextStep    *string
	Why         string // lý do / kết quả mong muốn
	Owner       string // ai làm
	WaitingOn   string // đang chờ ai/cái gì
	DueAt       string // hạn YYYY-MM-DD
	Constraints string // ràng buộc / cách làm
	UpdatedAt   time.Time
}

// TaskFields: các trường tuỳ chọn của task. nil = không đụng; con trỏ tới ""
// = xoá (NULL) khi update.
type TaskFields struct {
	NextStep, Why, Owner, WaitingOn, DueAt, Constraints *string
}

// cols: cặp (cột, giá trị) theo thứ tự cố định cho các field khác nil.
func (f TaskFields) cols() ([]string, []any) {
	var cs []string
	var vs []any
	add := func(c string, v *string) {
		if v == nil {
			return
		}
		cs = append(cs, c)
		if *v == "" {
			vs = append(vs, nil)
		} else {
			vs = append(vs, *v)
		}
	}
	add("next_step", f.NextStep)
	add("why", f.Why)
	add("owner", f.Owner)
	add("waiting_on", f.WaitingOn)
	add("due_at", f.DueAt)
	add("constraints", f.Constraints)
	return cs, vs
}

const taskCols = `id, space_id, title, status, next_step, why, owner, waiting_on, due_at, constraints, updated_at`

// InsertTask ghi task mới; đã có task open cùng space cùng title → không tạo
// trùng, trả id cũ với created=false (chống lặp khi job extract retry).
// nextStep nil = không có bước kế.
func (s *Store) InsertTask(ctx context.Context, spaceID int64, title string, nextStep *string, now time.Time) (int64, bool, error) {
	return s.InsertTaskWith(ctx, spaceID, title, TaskFields{NextStep: nextStep}, now)
}

// InsertTaskWith như InsertTask kèm trường 5W. Trùng task open cùng title
// (so sau NormTitle: hoa/thường, dấu, dấu câu) → chỉ điền các trường còn
// trống của task cũ (không ghi đè thông tin đã có).
func (s *Store) InsertTaskWith(ctx context.Context, spaceID int64, title string, f TaskFields, now time.Time) (int64, bool, error) {
	cs, vs := f.cols()
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := openTaskByNorm(ctx, tx, spaceID, title)
	switch {
	case err == nil:
		if len(cs) > 0 {
			sets := make([]string, len(cs))
			for i, c := range cs {
				sets[i] = c + "=COALESCE(" + c + ", ?)"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id=?`,
				append(vs, id)...); err != nil {
				return 0, false, err
			}
		}
		return id, false, tx.Commit()
	case errors.Is(err, sql.ErrNoRows):
	default:
		return 0, false, err
	}
	cols := append([]string{"space_id", "title", "status", "updated_at"}, cs...)
	args := append([]any{spaceID, title, "open", ts(now)}, vs...)
	res, err := tx.ExecContext(ctx, `INSERT INTO tasks(`+strings.Join(cols, ", ")+`) VALUES (?`+
		strings.Repeat(", ?", len(cols)-1)+`)`, args...)
	if err != nil {
		return 0, false, err
	}
	if id, err = res.LastInsertId(); err != nil {
		return 0, false, err
	}
	return id, true, tx.Commit()
}

// DueTasks: task open có hạn ≤ until (YYYY-MM-DD), hạn gần trước.
func (s *Store) DueTasks(ctx context.Context, spaceID int64, until string, limit int) ([]Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks
		WHERE space_id=? AND status='open' AND due_at IS NOT NULL AND due_at<=?
		ORDER BY due_at, id LIMIT ?`, spaceID, until, limit)
}

// WaitingTasks: task open đang chờ người/việc khác, lâu nhất trước.
func (s *Store) WaitingTasks(ctx context.Context, spaceID int64, limit int) ([]Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks
		WHERE space_id=? AND status='open' AND waiting_on IS NOT NULL
		ORDER BY updated_at, id LIMIT ?`, spaceID, limit)
}

func (s *Store) queryTasks(ctx context.Context, q string, args ...any) ([]Task, error) {
	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// OpenTasks: task status='open' của space, updated_at DESC.
func (s *Store) OpenTasks(ctx context.Context, spaceID int64, limit int) ([]Task, error) {
	return s.ListTasks(ctx, spaceID, "open", limit)
}

// ListTasks: task của space, status rỗng = không lọc, updated_at DESC.
func (s *Store) ListTasks(ctx context.Context, spaceID int64, status string, limit int) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE space_id=?`
	args := []any{spaceID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	return s.queryTasks(ctx, q, args...)
}

// TaskByID trả task theo id; id không tồn tại → sql.ErrNoRows.
func (s *Store) TaskByID(ctx context.Context, id int64) (Task, error) {
	return scanTask(s.DB().QueryRowContext(ctx,
		`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
}

// UpdateTask cập nhật status/next_step (nil = giữ nguyên, "" = xoá) + updated_at=now khi
// có ít nhất một field; không field nào → no-op trả task hiện tại.
// id không tồn tại → sql.ErrNoRows.
func (s *Store) UpdateTask(ctx context.Context, id int64, status *string, nextStep *string, now time.Time) (Task, error) {
	return s.UpdateTaskWith(ctx, id, status, TaskFields{NextStep: nextStep}, now, false)
}

// UpdateTaskWith cập nhật status + các trường 5W khác nil (con trỏ tới "" =
// xoá). touch=true buộc gia hạn updated_at dù không field nào đổi.
func (s *Store) UpdateTaskWith(ctx context.Context, id int64, status *string, f TaskFields, now time.Time, touch bool) (Task, error) {
	cs, vs := f.cols()
	if status != nil || len(cs) > 0 || touch {
		sets := []string{"updated_at=?"}
		args := []any{ts(now)}
		if status != nil {
			sets = append(sets, "status=?")
			args = append(args, *status)
		}
		for i, c := range cs {
			sets = append(sets, c+"=?")
			args = append(args, vs[i])
		}
		args = append(args, id)
		if _, err := s.DB().ExecContext(ctx, `UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...); err != nil {
			return Task{}, err
		}
	}
	return s.TaskByID(ctx, id)
}

// rowScanner chung cho *sql.Row / *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanTask(sc rowScanner) (Task, error) {
	var t Task
	var nextStep, why, owner, waiting, due, cons sql.NullString
	var updatedAt string
	if err := sc.Scan(&t.ID, &t.SpaceID, &t.Title, &t.Status, &nextStep,
		&why, &owner, &waiting, &due, &cons, &updatedAt); err != nil {
		return Task{}, err
	}
	if nextStep.Valid {
		t.NextStep = &nextStep.String
	}
	t.Why, t.Owner, t.WaitingOn, t.DueAt, t.Constraints = why.String, owner.String, waiting.String, due.String, cons.String
	t.UpdatedAt = parseTS(updatedAt)
	return t, nil
}

// AllTasksForExport: mọi task (mọi space, mọi status), theo id.
func (s *Store) AllTasksForExport(ctx context.Context) ([]Task, error) {
	return s.queryTasks(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY id`)
}

// DropTask chuyển task sang status='dropped'; false nếu id không tồn tại hoặc
// đã dropped từ trước.
func (s *Store) DropTask(ctx context.Context, id int64, now time.Time) (bool, error) {
	res, err := s.DB().ExecContext(ctx,
		`UPDATE tasks SET status='dropped', updated_at=? WHERE id=? AND status!='dropped'`, ts(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// openTaskByNorm tìm task open của space có tiêu đề trùng sau NormTitle.
func openTaskByNorm(ctx context.Context, tx *sql.Tx, spaceID int64, title string) (int64, error) {
	want := NormTitle(title)
	rows, err := tx.QueryContext(ctx, `SELECT id, title FROM tasks WHERE space_id=? AND status='open' ORDER BY id`, spaceID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			return 0, err
		}
		if t == title || NormTitle(t) == want {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return 0, sql.ErrNoRows
}

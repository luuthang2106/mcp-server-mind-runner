package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// MaxAttempts: quá số lần này job thành dead (không retry nữa).
const MaxAttempts = 5

// Job tương ứng một dòng bảng jobs.
type Job struct {
	ID        int64
	Type      string
	Payload   json.RawMessage
	State     string
	Attempts  int
	RunAfter  time.Time
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// backoff: 30s * 2^(attempts-1), trần 1h.
func backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := 30 * time.Second << (attempts - 1)
	if d > time.Hour || d <= 0 {
		return time.Hour
	}
	return d
}

// Enqueue thêm job state=queued; payload marshal JSON (nil → '{}').
// Idempotent theo (type, payload): nếu đã có job cùng loại + payload còn đang
// chờ/chạy/retry (queued|failed|running) thì trả id job đó, không tạo trùng.
func (s *Store) Enqueue(ctx context.Context, jobType string, payload any, runAfter time.Time) (int64, error) {
	data := []byte("{}")
	if payload != nil {
		var err error
		if data, err = json.Marshal(payload); err != nil {
			return 0, err
		}
	}
	var existing int64
	err := s.DB().QueryRowContext(ctx,
		`SELECT id FROM jobs WHERE type=? AND payload=? AND state IN ('queued','failed','running') LIMIT 1`,
		jobType, string(data)).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	now := ts(time.Now())
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO jobs(type,payload,state,attempts,run_after,created_at,updated_at)
		 VALUES(?,?,'queued',0,?,?,?)`,
		jobType, string(data), ts(runAfter), now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// StaleRunning: job ở state running lâu hơn mức này coi như process chạy nó đã
// chết (crash, bị kill, máy ngủ) → được claim lại. Job dài nhất (transcribe)
// bị chặn bởi timeout HTTP 120s × vài lượt nên 30 phút là dư.
const StaleRunning = 30 * time.Minute

// ClaimJob nhận job tới hạn: queued, hoặc failed đã qua run_after (retry —
// dead/attempts>=MaxAttempts không bao giờ được claim lại), hoặc running bị kẹt
// quá StaleRunning (process cũ chết giữa chừng).
// Hai bước chống race giữa nhiều process: SELECT id rồi UPDATE có điều kiện state.
func (s *Store) ClaimJob(ctx context.Context, now time.Time) (*Job, error) {
	var id int64
	err := s.DB().QueryRowContext(ctx,
		`SELECT id FROM jobs
		 WHERE (state IN ('queued','failed') AND run_after<=?)
		    OR (state='running' AND updated_at<?)
		 ORDER BY id LIMIT 1`,
		ts(now), ts(now.Add(-StaleRunning))).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := s.DB().ExecContext(ctx,
		`UPDATE jobs SET state='running', updated_at=?, attempts=attempts+1
		 WHERE id=? AND ((state IN ('queued','failed') AND run_after<=?)
		    OR (state='running' AND updated_at<?))`,
		ts(now), id, ts(now), ts(now.Add(-StaleRunning)))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil // thua race
	}
	return s.jobByID(ctx, id)
}

func (s *Store) jobByID(ctx context.Context, id int64) (*Job, error) {
	row := s.DB().QueryRowContext(ctx,
		`SELECT id, type, payload, state, attempts, run_after, COALESCE(last_error,''), created_at, updated_at
		 FROM jobs WHERE id=?`, id)
	var j Job
	var payload, runAfter, createdAt, updatedAt string
	if err := row.Scan(&j.ID, &j.Type, &payload, &j.State, &j.Attempts,
		&runAfter, &j.LastError, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	j.Payload = json.RawMessage(payload)
	j.RunAfter, j.CreatedAt, j.UpdatedAt = parseTS(runAfter), parseTS(createdAt), parseTS(updatedAt)
	return &j, nil
}

// CompleteJob đánh dấu job xong.
func (s *Store) CompleteJob(ctx context.Context, id int64) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE jobs SET state='done', updated_at=? WHERE id=?`, ts(time.Now()), id)
	return err
}

// FailJob ghi lỗi + lên lịch retry theo backoff; đủ MaxAttempts → dead.
func (s *Store) FailJob(ctx context.Context, now time.Time, id int64, lastErr string) error {
	var attempts int
	if err := s.DB().QueryRowContext(ctx, `SELECT attempts FROM jobs WHERE id=?`, id).Scan(&attempts); err != nil {
		return err
	}
	if attempts >= MaxAttempts {
		_, err := s.DB().ExecContext(ctx,
			`UPDATE jobs SET state='dead', last_error=?, updated_at=? WHERE id=?`, lastErr, ts(now), id)
		return err
	}
	_, err := s.DB().ExecContext(ctx,
		`UPDATE jobs SET state='failed', run_after=?, last_error=?, updated_at=? WHERE id=?`,
		ts(now.Add(backoff(attempts))), lastErr, ts(now), id)
	return err
}

// DeferJob trả job về queued và lùi run_after mà KHÔNG tính một lượt thử
// (vd thiếu API key ở process maintenance — process MCP có key sẽ chạy sau).
func (s *Store) DeferJob(ctx context.Context, now time.Time, id int64, until time.Time, reason string) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE jobs SET state='queued', attempts=MAX(attempts-1,0), run_after=?, last_error=?, updated_at=?
		 WHERE id=?`, ts(until), reason, ts(now), id)
	return err
}

// MarkDead đánh job chết hẳn ngay (lỗi permanent — không cần chờ đủ lượt retry).
func (s *Store) MarkDead(ctx context.Context, id int64, lastErr string, now time.Time) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE jobs SET state='dead', last_error=?, updated_at=? WHERE id=?`, lastErr, ts(now), id)
	return err
}

// RetryDead đưa mọi job dead về queued để chạy lại (đếm lại từ 0, chạy ngay).
func (s *Store) RetryDead(ctx context.Context, now time.Time) (int, error) {
	res, err := s.DB().ExecContext(ctx,
		`UPDATE jobs SET state='queued', attempts=0, run_after=?, last_error='', updated_at=? WHERE state='dead'`, ts(now), ts(now))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// JobCounts đếm job theo state.
func (s *Store) JobCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB().QueryContext(ctx, `SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		counts[state] = n
	}
	return counts, rows.Err()
}

// DeadJobs trả các job dead mới nhất trước.
func (s *Store) DeadJobs(ctx context.Context, limit int) ([]Job, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id, type, payload, state, attempts, run_after, COALESCE(last_error,''), created_at, updated_at
		 FROM jobs WHERE state='dead' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var j Job
		var payload, runAfter, createdAt, updatedAt string
		if err := rows.Scan(&j.ID, &j.Type, &payload, &j.State, &j.Attempts,
			&runAfter, &j.LastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		j.Payload = json.RawMessage(payload)
		j.RunAfter, j.CreatedAt, j.UpdatedAt = parseTS(runAfter), parseTS(createdAt), parseTS(updatedAt)
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestEnqueueClaimComplete(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id, err := s.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": 7}, now)
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id=%d", id)
	}

	// chưa tới run_after → chưa claim được
	if job, err := s.ClaimJob(ctx, now.Add(-time.Second)); err != nil || job != nil {
		t.Fatalf("job=%v err=%v, muốn (nil, nil)", job, err)
	}

	job, err := s.ClaimJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != id || job.Type != "embed_chunk" || job.State != "running" || job.Attempts != 1 {
		t.Fatalf("job=%+v", job)
	}
	var p struct {
		NoteID int64 `json:"note_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.NoteID != 7 {
		t.Fatalf("payload=%s err=%v", job.Payload, err)
	}

	// đang running → không claim lại
	if job, err := s.ClaimJob(ctx, now); err != nil || job != nil {
		t.Fatalf("claim lại khi running: %v %v", job, err)
	}

	if err := s.CompleteJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	counts, err := s.JobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["done"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestClaimEmpty(t *testing.T) {
	s := openMigrated(t)
	job, err := s.ClaimJob(context.Background(), time.Now())
	if err != nil || job != nil {
		t.Fatalf("job=%v err=%v, muốn (nil, nil)", job, err)
	}
}

func TestFailBackoffThenDead(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	id, err := s.Enqueue(ctx, "noop", nil, now)
	if err != nil {
		t.Fatal(err)
	}

	// fail lần 1 (attempts đã tăng lúc claim → 1): backoff 30s
	if job, err := s.ClaimJob(ctx, now); err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	if err := s.FailJob(ctx, now, id, "boom"); err != nil {
		t.Fatal(err)
	}
	assertRow := func(wantState, wantRunAfter, wantErr string, wantAttempts int) {
		t.Helper()
		var state, runAfter, lastErr string
		var attempts int
		if err := s.DB().QueryRowContext(ctx,
			`SELECT state, run_after, COALESCE(last_error,''), attempts FROM jobs WHERE id=?`, id).
			Scan(&state, &runAfter, &lastErr, &attempts); err != nil {
			t.Fatal(err)
		}
		if state != wantState || runAfter != wantRunAfter || lastErr != wantErr || attempts != wantAttempts {
			t.Fatalf("state=%s run_after=%s err=%q attempts=%d, muốn %s %s %q %d",
				state, runAfter, lastErr, attempts, wantState, wantRunAfter, wantErr, wantAttempts)
		}
	}
	assertRow("failed", ts(now.Add(30*time.Second)), "boom", 1)

	// chưa qua backoff → không claim; qua backoff → claim lại được
	if job, err := s.ClaimJob(ctx, now.Add(29*time.Second)); err != nil || job != nil {
		t.Fatalf("trước backoff: %v %v", job, err)
	}
	t1 := now.Add(30 * time.Second)
	job, err := s.ClaimJob(ctx, t1)
	if err != nil || job == nil || job.Attempts != 2 {
		t.Fatalf("sau backoff: %+v %v", job, err)
	}

	// các lần fail tiếp theo: 60s, 120s, 240s — hết lần 4 → attempts=5 → dead
	t2 := t1.Add(60 * time.Second)
	if err := s.FailJob(ctx, t1, id, "l2"); err != nil {
		t.Fatal(err)
	}
	assertRow("failed", ts(t2), "l2", 2)
	if _, err := s.ClaimJob(ctx, t2); err != nil {
		t.Fatal(err)
	}
	t3 := t2.Add(120 * time.Second)
	if err := s.FailJob(ctx, t2, id, "l3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimJob(ctx, t3); err != nil {
		t.Fatal(err)
	}
	t4 := t3.Add(240 * time.Second)
	if err := s.FailJob(ctx, t3, id, "l4"); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(ctx, t4); err != nil || job == nil || job.Attempts != 5 {
		t.Fatalf("claim lần 5: %+v %v", job, err)
	}
	if err := s.FailJob(ctx, t4, id, "l5"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DB().QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dead" {
		t.Fatalf("state=%s, muốn dead", state)
	}

	// dead không bao giờ claim lại
	if job, err := s.ClaimJob(ctx, t4.Add(24*time.Hour)); err != nil || job != nil {
		t.Fatalf("claim sau dead: %v %v", job, err)
	}

	counts, err := s.JobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["dead"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	dead, err := s.DeadJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 || dead[0].Type != "noop" || dead[0].LastError != "l5" || dead[0].Attempts != 5 {
		t.Fatalf("dead=%+v", dead)
	}
}

func TestEnqueueDedupeActive(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	a, err := s.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": 1}, now)
	if err != nil || b != a {
		t.Fatalf("dedupe: a=%d b=%d err=%v", a, b, err)
	}
	if c, _ := s.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": 2}, now); c == a {
		t.Fatal("payload khác phải tạo job mới")
	}
	// job xong → enqueue lại tạo job mới
	job, _ := s.ClaimJob(ctx, now)
	if err := s.CompleteJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": 1}, now); d == a {
		t.Fatal("job done không được chặn enqueue mới")
	}
}

func TestClaimStaleRunningAndDefer(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	id, _ := s.Enqueue(ctx, "x", nil, now)
	if j, _ := s.ClaimJob(ctx, now); j == nil || j.ID != id {
		t.Fatal("claim lần đầu")
	}
	// process chết: còn trong ngưỡng → không claim lại
	if j, _ := s.ClaimJob(ctx, now.Add(StaleRunning-time.Minute)); j != nil {
		t.Fatalf("claim sớm: %+v", j)
	}
	j, err := s.ClaimJob(ctx, now.Add(StaleRunning+time.Minute))
	if err != nil || j == nil || j.ID != id || j.Attempts != 2 {
		t.Fatalf("reclaim stale: %+v err=%v", j, err)
	}
	// defer: trả về queued, không tính lượt
	later := now.Add(time.Hour)
	if err := s.DeferJob(ctx, now, id, later, "no key"); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.ClaimJob(ctx, later.Add(-time.Second)); j != nil {
		t.Fatal("defer chưa tới hạn mà claim được")
	}
	j, _ = s.ClaimJob(ctx, later)
	if j == nil || j.Attempts != 2 {
		t.Fatalf("sau defer: %+v", j)
	}
}

func TestPurgeZeroDaysKeepsEverything(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	now := time.Now()
	if _, _, err := s.InsertTask(ctx, 1, "t", nil, now.AddDate(-2, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE tasks SET status='done'`); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Purge(ctx, now, Retention{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tasks != 0 || rep.Events != 0 {
		t.Fatalf("retention 0 phải tắt purge tầng đó: %+v", rep)
	}
}

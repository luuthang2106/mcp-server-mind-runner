package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mind-runner/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background(), filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestRunProcessesAndFails: job ok + job lỗi → cả hai được xử lý; job lỗi
// failed với run_after = now + backoff(30s); payload truyền nguyên bytes.
func TestRunProcessesAndFails(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if _, err := st.Enqueue(ctx, "ok_job", map[string]string{"k": "v"}, base); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue(ctx, "fail_job", map[string]int{"n": 1}, base); err != nil {
		t.Fatal(err)
	}

	var gotPayload json.RawMessage
	reg := NewRegistry()
	reg.Register("ok_job", func(_ context.Context, p json.RawMessage) error {
		gotPayload = p
		return nil
	})
	reg.Register("fail_job", func(context.Context, json.RawMessage) error {
		return errors.New("bùm")
	})

	n, err := Run(ctx, st, reg, 0, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("n=%d, muốn 2", n)
	}
	var m map[string]string
	if err := json.Unmarshal(gotPayload, &m); err != nil || m["k"] != "v" {
		t.Fatalf("payload=%s err=%v", gotPayload, err)
	}
	counts, err := st.JobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["done"] != 1 || counts["failed"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	var runAfter, lastErr string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT run_after, last_error FROM jobs WHERE type='fail_job'`).Scan(&runAfter, &lastErr); err != nil {
		t.Fatal(err)
	}
	want := store.TS(base.Add(30 * time.Second))
	if runAfter != want {
		t.Fatalf("run_after=%s, muốn %s", runAfter, want)
	}
	if lastErr != "bùm" {
		t.Fatalf("last_error=%q", lastErr)
	}
}

// TestUnknownJobTypeDiesAfterFive: job type chưa đăng ký → mỗi lần Run tăng
// attempts; sau 5 lần (dịch clock qua backoff) → dead và không claim lại.
func TestUnknownJobTypeDiesAfterFive(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if _, err := st.Enqueue(ctx, "type_lạ", nil, now); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	clock := func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if _, err := Run(ctx, st, reg, 0, clock); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		now = now.Add(time.Hour) // vượt mọi backoff
	}
	counts, err := st.JobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["dead"] != 1 {
		t.Fatalf("counts=%v, muốn dead=1", counts)
	}
	var lastErr string
	if err := st.DB().QueryRowContext(ctx, `SELECT last_error FROM jobs`).Scan(&lastErr); err != nil {
		t.Fatal(err)
	}
	if lastErr != "no handler for job type type_lạ" {
		t.Fatalf("last_error=%q", lastErr)
	}
	// dead không được claim lại
	n, err := Run(ctx, st, reg, 0, clock)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, muốn 0", n, err)
	}
}

// TestMaxLimits: max=1 với 2 job → chỉ 1 job chạy.
func TestMaxLimits(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		// payload khác nhau — Enqueue dedupe theo (type,payload)
		if _, err := st.Enqueue(ctx, "j", map[string]int{"i": i}, now); err != nil {
			t.Fatal(err)
		}
	}
	reg := NewRegistry()
	reg.Register("j", func(context.Context, json.RawMessage) error { return nil })
	n, err := Run(ctx, st, reg, 1, func() time.Time { return now })
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, muốn 1", n, err)
	}
	counts, _ := st.JobCounts(ctx)
	if counts["done"] != 1 || counts["queued"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
}

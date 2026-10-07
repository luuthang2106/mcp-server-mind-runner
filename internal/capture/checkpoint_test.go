package capture

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mind-runner/internal/store"
)

func newStore(t *testing.T) *store.Store {
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

func TestReadDeltaAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("aaa\nbbb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, off, reset, err := ReadDelta(path, 0)
	if err != nil || reset {
		t.Fatalf("reset=%v err=%v", reset, err)
	}
	if string(data) != "aaa\nbbb\n" || off != 8 {
		t.Fatalf("data=%q off=%d", data, off)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ccc\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	data2, off2, reset2, err := ReadDelta(path, off)
	if err != nil || reset2 {
		t.Fatalf("reset2=%v err=%v", reset2, err)
	}
	if string(data2) != "ccc\n" || off2 != 12 {
		t.Fatalf("data2=%q off2=%d", data2, off2)
	}
}

func TestReadDeltaResetWhenShorter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, off, reset, err := ReadDelta(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reset || string(data) != "short\n" || off != 6 {
		t.Fatalf("data=%q off=%d reset=%v", data, off, reset)
	}
}

func TestStopEndToEnd(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	spoolDir := filepath.Join(t.TempDir(), "spool")

	// copy fixture ra file tạm để append được
	src, err := os.ReadFile("testdata/session_basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tp, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSessionStart(ctx, "sess-1", "claude-code", 1, &tp, t0); err != nil {
		t.Fatal(err)
	}

	sess, err := st.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	rawID, err := Stop(ctx, st, sess, 90, spoolDir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if rawID == nil {
		t.Fatal("rawID nil")
	}

	// raw row: seq 1, gzip == nội dung file, offset = size, 1 job extract_session
	var blob []byte
	if err := st.DB().QueryRowContext(ctx,
		`SELECT content FROM session_raw WHERE session_id='sess-1' AND seq=1`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, src) {
		t.Fatalf("plain=%q", plain)
	}
	sess, _ = st.GetSession(ctx, "sess-1")
	if sess.TranscriptOffset != int64(len(src)) {
		t.Fatalf("offset=%d, muốn %d", sess.TranscriptOffset, len(src))
	}
	var jobType, payload, state string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT type, payload, state FROM jobs`).Scan(&jobType, &payload, &state); err != nil {
		t.Fatal(err)
	}
	if jobType != "extract_session" || state != "queued" || payload != `{"session_id":"sess-1"}` {
		t.Fatalf("job type=%s payload=%s state=%s", jobType, payload, state)
	}

	// Stop lần 2 sau khi append → raw seq 2
	f, err := os.OpenFile(tp, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"type\":\"user\"}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	sess, _ = st.GetSession(ctx, "sess-1")
	rawID2, err := Stop(ctx, st, sess, 90, spoolDir, t0)
	if err != nil || rawID2 == nil {
		t.Fatalf("rawID2=%v err=%v", rawID2, err)
	}
	var seq int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT seq FROM session_raw WHERE id=?`, *rawID2).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 2 {
		t.Fatalf("seq=%d, muốn 2", seq)
	}

	// không có delta mới → nil, không thêm raw/job
	sess, _ = st.GetSession(ctx, "sess-1")
	rawID3, err := Stop(ctx, st, sess, 90, spoolDir, t0)
	if err != nil || rawID3 != nil {
		t.Fatalf("rawID3=%v err=%v", rawID3, err)
	}
	var nRaw, nJobs int
	st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM session_raw`).Scan(&nRaw)
	st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&nJobs)
	if nRaw != 2 || nJobs != 1 { // 2 lượt cùng phiên gộp vào 1 job
		t.Fatalf("raw=%d jobs=%d", nRaw, nJobs)
	}
}

func TestReadDeltaKeepsPartialLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte("{\"a\":1}\n{\"b\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, off, _, err := ReadDelta(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"a\":1}\n" || off != 8 {
		t.Fatalf("data=%q off=%d", data, off)
	}
	// chưa có dòng hoàn chỉnh mới → không tiến offset
	if data, off2, _, _ := ReadDelta(p, off); len(data) != 0 || off2 != off {
		t.Fatalf("partial: data=%q off=%d", data, off2)
	}
}

func TestStopTwiceSameOffsetNoDuplicate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tp, []byte("{\"x\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSessionStart(ctx, "s", "claude-code", 1, &tp, t0); err != nil {
		t.Fatal(err)
	}
	// hai hook (Stop + SessionEnd) cùng đọc session ở offset 0
	a, _ := st.GetSession(ctx, "s")
	b, _ := st.GetSession(ctx, "s")
	if id, err := Stop(ctx, st, a, 90, t.TempDir(), t0); err != nil || id == nil {
		t.Fatalf("stop 1: id=%v err=%v", id, err)
	}
	if id, err := Stop(ctx, st, b, 90, t.TempDir(), t0); err != nil || id != nil {
		t.Fatalf("stop 2 phải bỏ qua: id=%v err=%v", id, err)
	}
	var n int
	_ = st.DB().QueryRow(`SELECT count(*) FROM session_raw`).Scan(&n)
	if n != 1 {
		t.Fatalf("raw=%d, muốn 1", n)
	}
}

func TestSpoolSkippedWhenRecapturedLater(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tp, []byte("{\"x\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSessionStart(ctx, "s", "claude-code", 1, &tp, t0); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "spool")
	// lần Stop đầu bị DB bận → spool (offset 0→8), offset DB vẫn 0
	if err := WriteWithOffset(dir, "s", []byte("blob"), 0, 8); err != nil {
		t.Fatal(err)
	}
	// Stop sau đó chốt lại cùng đoạn
	sess, _ := st.GetSession(ctx, "s")
	if id, err := Stop(ctx, st, sess, 90, dir, t0); err != nil || id == nil {
		t.Fatalf("stop: %v %v", id, err)
	}
	n, err := MergeSpool(ctx, st, dir, 90, t0)
	if err != nil || n != 0 {
		t.Fatalf("merge n=%d err=%v, muốn bỏ file trùng", n, err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("spool còn file: %v", left)
	}
}

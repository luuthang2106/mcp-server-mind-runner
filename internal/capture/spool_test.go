package capture

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpoolWriteMerge(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), "spool")

	// dir chưa tồn tại → merge im lặng (0, nil)
	if n, err := MergeSpool(ctx, st, dir, 90, t0); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	tp := "/tmp/t.jsonl"
	if err := st.UpsertSessionStart(ctx, "sess-1", "claude-code", 1, &tp, t0); err != nil {
		t.Fatal(err)
	}

	blob1 := []byte("blob-1")
	blob2 := []byte("blob-2")
	if err := Write(dir, "sess-1", blob1); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "sess-1", blob2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}

	n, err := MergeSpool(ctx, st, dir, 90, t0)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var rawCount int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM session_raw`).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	counts, err := st.JobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// cùng phiên → gộp vào một job extract
	if rawCount != 2 || counts["queued"] != 1 {
		t.Fatalf("raw=%d counts=%v", rawCount, counts)
	}
	// cả 2 blob còn nguyên trong DB
	var blobs [][]byte
	rows, err := st.DB().QueryContext(ctx, `SELECT content FROM session_raw ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, b)
	}
	if len(blobs) != 2 || !bytes.Contains(blobs[0], blob1) && !bytes.Contains(blobs[1], blob1) {
		t.Fatalf("blobs=%q", blobs)
	}

	// dir rỗng sau merge; merge lại → 0
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dir còn %d file", len(entries))
	}
	if n, err := MergeSpool(ctx, st, dir, 90, t0); err != nil || n != 0 {
		t.Fatalf("merge lại: n=%d err=%v", n, err)
	}
}

func TestWriteCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chưa", "tồn-tại")
	if err := Write(dir, "sess-1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("dir: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
}

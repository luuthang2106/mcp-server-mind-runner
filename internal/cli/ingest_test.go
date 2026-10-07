package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/store"
)

// TestRunIngest: ingest file .md → in note_id; chạy lặp → cùng id + "(đã có)";
// kind caption / file thiếu / space sai → exit 1 kèm thông báo; media → hàng đợi media.
func TestRunIngest(t *testing.T) {
	_, dataDir := setupFresh(t)

	path := filepath.Join(t.TempDir(), "tài liệu.md")
	if err := os.WriteFile(path, []byte("nội dung cần nạp"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errb bytes.Buffer
		code := RunIngest(args, &out, &errb, os.Getenv)
		return code, out.String(), errb.String()
	}

	code, out, errb := run(path)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb)
	}
	var id1 int64
	if _, err := fmt.Sscanf(out, "note_id %d", &id1); err != nil || id1 <= 0 {
		t.Fatalf("out=%q err=%v", out, err)
	}

	code, out, errb = run(path)
	if code != 0 {
		t.Fatalf("lần 2 exit=%d stderr=%s", code, errb)
	}
	var id2 int64
	if _, err := fmt.Sscanf(out, "note_id %d", &id2); err != nil || id2 != id1 || !strings.Contains(out, "đã có") {
		t.Fatalf("lần 2 out=%q id1=%d err=%v", out, id1, err)
	}

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("notes=%d, muốn 1", n)
	}

	// --kind decision ghi đúng kind
	path2 := filepath.Join(t.TempDir(), "qd.md")
	if err := os.WriteFile(path2, []byte("chốt dùng sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errb = run("--kind", "decision", path2); code != 0 {
		t.Fatalf("kind decision exit=%d stderr=%s", code, errb)
	}
	var kind string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT kind FROM notes WHERE text='chốt dùng sqlite'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "decision" {
		t.Fatalf("kind=%q", kind)
	}

	// kind caption → exit 1
	if code, _, errb = run("--kind", "caption", path); code != 1 || !strings.Contains(errb, "caption") {
		t.Fatalf("caption: exit=%d stderr=%q", code, errb)
	}
	// file thiếu → exit 1
	if code, _, _ = run(filepath.Join(t.TempDir(), "khong-co.md")); code != 1 {
		t.Fatalf("file thiếu: exit=%d", code)
	}
	// space sai → exit 1 + liệt kê hợp lệ
	if code, _, errb = run("--space", "không-có", path); code != 1 || !strings.Contains(errb, "personal") {
		t.Fatalf("space sai: exit=%d stderr=%q", code, errb)
	}
	// đuôi media → vào hàng đợi media, không nạp như text
	mediaPath := filepath.Join(t.TempDir(), "ghi-am.m4a")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errb = run(mediaPath); code != 0 || !strings.Contains(out, "media ") || !strings.Contains(out, "queued") {
		t.Fatalf("media: exit=%d stdout=%q stderr=%q", code, out, errb)
	}
	if code, out, _ = run(mediaPath); code != 0 || !strings.Contains(out, "(đã có)") {
		t.Fatalf("media lặp: exit=%d stdout=%q", code, out)
	}
}

// TestRunIngestFlagsAfterPath: usage quảng cáo `ingest <path> [--kind …]` —
// flags đứng sau positional phải hoạt động (Go flag mặc định dừng ở arg đầu).
func TestRunIngestFlagsAfterPath(t *testing.T) {
	_, dataDir := setupFresh(t)
	path := filepath.Join(t.TempDir(), "sau-flag.md")
	if err := os.WriteFile(path, []byte("nội dung sau flag"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := RunIngest([]string{path, "--kind", "decision", "--space", "work"}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var kind, space string
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT n.kind, s.name FROM notes n JOIN spaces s ON s.id=n.space_id WHERE n.text='nội dung sau flag'`).
		Scan(&kind, &space); err != nil {
		t.Fatal(err)
	}
	if kind != "decision" || space != "work" {
		t.Fatalf("kind=%q space=%q", kind, space)
	}
}

package brain

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIngestText: nạp file hai lần → 1 note (content-hash), chunks/job không
// nhân; kind mặc định note, kind decision giữ vĩnh viễn; kind media (caption)
// không nhận; file thiếu/rỗng → error; session_id NULL, source ingest:<path>.
func TestIngestText(t *testing.T) {
	st := newStore(t)
	b := New(st, nil, testCfg())
	ctx := context.Background()
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "ghi-chu.md")
	if err := os.WriteFile(path, []byte("# Tiêu đề\n\nnội dung nghiên cứu"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := b.IngestText(ctx, IngestTextParams{Path: path, SpaceID: spaceID})
	if err != nil || !res.Fresh || res.NoteID <= 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	var kind, source string
	var sessionID sql.NullString
	if err := st.DB().QueryRowContext(ctx,
		`SELECT kind, source, session_id FROM notes WHERE id=?`, res.NoteID).
		Scan(&kind, &source, &sessionID); err != nil {
		t.Fatal(err)
	}
	if kind != "document" || source != "ingest:"+path || sessionID.Valid {
		t.Fatalf("kind=%q source=%q session=%+v", kind, source, sessionID)
	}
	// tag tự sinh từ tên file + meta title/ref
	n, err := st.FetchNote(ctx, res.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Tags) != 1 || n.Tags[0] != "file:ghi-chu" || n.Meta.Title != "ghi-chu.md" || n.Meta.Ref != path {
		t.Fatalf("tags=%v meta=%+v", n.Tags, n.Meta)
	}
	countOf := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := st.DB().QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	chunks := countOf(`SELECT COUNT(*) FROM chunks WHERE note_id=?`, res.NoteID)
	jobs := countOf(`SELECT COUNT(*) FROM jobs WHERE type='embed_chunk' AND state='queued'`)
	if chunks == 0 || jobs != 1 {
		t.Fatalf("chunks=%d jobs=%d", chunks, jobs)
	}

	// nạp lặp → cùng id, Created=false, chunks/job không nhân
	res2, err := b.IngestText(ctx, IngestTextParams{Path: path, SpaceID: spaceID})
	if err != nil || res2.Fresh || res2.NoteID != res.NoteID {
		t.Fatalf("lần 2: res=%+v err=%v", res2, err)
	}
	if got := countOf(`SELECT COUNT(*) FROM chunks WHERE note_id=?`, res.NoteID); got != chunks {
		t.Fatalf("chunks nhân: %d → %d", chunks, got)
	}
	if got := countOf(`SELECT COUNT(*) FROM jobs WHERE type='embed_chunk' AND state='queued'`); got != jobs {
		t.Fatalf("jobs nhân: %d → %d", jobs, got)
	}

	// kind decision → lưu đúng kind
	path2 := filepath.Join(t.TempDir(), "quyet-dinh.md")
	if err := os.WriteFile(path2, []byte("chọn SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	res3, err := b.IngestText(ctx, IngestTextParams{Path: path2, SpaceID: spaceID, Kind: "decision"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT kind FROM notes WHERE id=?`, res3.NoteID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "decision" {
		t.Fatalf("kind=%q", kind)
	}

	// kind media không nhận từ ingest
	if _, err := b.IngestText(ctx, IngestTextParams{Path: path, SpaceID: spaceID, Kind: "caption"}); err == nil || !strings.Contains(err.Error(), "caption") {
		t.Fatalf("kind caption: err=%v", err)
	}
	// file thiếu
	if _, err := b.IngestText(ctx, IngestTextParams{Path: filepath.Join(t.TempDir(), "khong-co.md"), SpaceID: spaceID}); err == nil {
		t.Fatal("file thiếu phải error")
	}
	// file rỗng
	empty := filepath.Join(t.TempDir(), "rong.txt")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.IngestText(ctx, IngestTextParams{Path: empty, SpaceID: spaceID}); err == nil || !strings.Contains(err.Error(), "rỗng") {
		t.Fatalf("file rỗng: err=%v", err)
	}
}

func TestFileTag(t *testing.T) {
	for in, want := range map[string]string{
		"/a/Spec Auth v2.md":   "file:spec-auth-v2",
		"/a/Họp tuần (T3).txt": "file:họp-tuần-t3",
		"/a/---.md":            "",
	} {
		if got := FileTag(in); got != want {
			t.Errorf("FileTag(%q)=%q want %q", in, got, want)
		}
	}
}

package brain

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mind-runner/internal/store"
)

// TestExportVault: fixture tất định → cây file + nội dung note/tasks/relations
// byte-exact (so trong test); note đã xoá mềm không có file.
func TestExportVault(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	tx := func(y int, mo time.Month, d, h int) time.Time { return time.Date(y, mo, d, h, 0, 0, 0, time.UTC) }

	n1, _, err := st.UpsertNote(ctx, &store.Note{
		SpaceID: 1, Kind: "fact", Text: "SQLite local-first",
		Tags: []string{"db", "mind-runner"}, Source: "test",
		CreatedAt: tx(2026, 10, 1, 10), UpdatedAt: tx(2026, 10, 5, 10),
	})
	if err != nil {
		t.Fatal(err)
	}
	sess := "sess-1"
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at) VALUES('sess-1','claude-desktop',2,?,?)`,
		tx(2026, 10, 2, 9).Format(time.RFC3339Nano), tx(2026, 10, 2, 9).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertNote(ctx, &store.Note{
		SpaceID: 2, Kind: "decision", Text: "Chọn vault export\nDòng hai",
		Source: "ingest:/tmp/tài liệu.md", SessionID: &sess,
		CreatedAt: tx(2026, 10, 2, 9), UpdatedAt: tx(2026, 10, 2, 9),
	}); err != nil {
		t.Fatal(err)
	}
	del, _, err := st.UpsertNote(ctx, &store.Note{
		SpaceID: 1, Kind: "note", Text: "bí mật đã xoá", Source: "test",
		CreatedAt: tx(2026, 10, 3, 9), UpdatedAt: tx(2026, 10, 3, 9),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SoftDeleteNote(ctx, del, tx(2026, 10, 4, 9)); err != nil || !ok {
		t.Fatalf("xoá mềm: ok=%v err=%v", ok, err)
	}

	next := "viết README"
	if _, _, err := st.InsertTask(ctx, 1, "Chuẩn bị demo", &next, tx(2026, 10, 5, 9)); err != nil {
		t.Fatal(err)
	}
	id2, _, err := st.InsertTask(ctx, 1, "Nộp báo cáo", nil, tx(2026, 10, 4, 9))
	if err != nil {
		t.Fatal(err)
	}
	done := "done"
	if _, err := st.UpdateTask(ctx, id2, &done, nil, tx(2026, 10, 4, 9)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.InsertTask(ctx, 2, "Họp nhóm", nil, tx(2026, 10, 5, 9)); err != nil {
		t.Fatal(err)
	}

	if _, err := st.InsertRelation(ctx, 1, "Viết tài liệu", "SQLite local-first", "mentions", &n1, tx(2026, 10, 5, 11)); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	rep, err := New(st, nil, testCfg()).Export(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dir != out || rep.Notes != 2 || rep.Tasks != 3 || rep.Relations != 1 {
		t.Fatalf("rep=%+v", rep)
	}

	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Fatalf("thiếu file %s: %v", rel, err)
		}
		return string(data)
	}

	// note n1 (id 1) byte-exact
	wantN1 := "---\nkind: fact\ntags: [\"db\",\"mind-runner\"]\n" +
		"created: 2026-10-01T10:00:00Z\nupdated: 2026-10-05T10:00:00Z\nsource: \"test\"\n---\n\nSQLite local-first\n"
	if got := read("notes/personal/fact/1-sqlite-local-first.md"); got != wantN1 {
		t.Fatalf("note1:\n--- got ---\n%s\n--- want ---\n%s", got, wantN1)
	}
	// note n2 (id 2): session + source có dấu, slug dòng đầu
	wantN2 := "---\nkind: decision\ntags: []\n" +
		"created: 2026-10-02T09:00:00Z\nupdated: 2026-10-02T09:00:00Z\nsource: \"ingest:/tmp/tài liệu.md\"\nsession: sess-1\n---\n\nChọn vault export\nDòng hai\n"
	if got := read("notes/work/decision/2-chọn-vault-export.md"); got != wantN2 {
		t.Fatalf("note2:\n--- got ---\n%s\n--- want ---\n%s", got, wantN2)
	}
	// note đã xoá mềm → không có file (thư mục kind note/ không tồn tại)
	if _, err := os.Stat(filepath.Join(out, "notes/personal/note")); !os.IsNotExist(err) {
		t.Fatalf("note xoá mềm vẫn được export (err=%v)", err)
	}

	// tasks theo space, byte-exact
	wantPersonal := "# Việc — personal\n\n- [ ] Chuẩn bị demo — bước kế: viết README\n- [x] Nộp báo cáo\n"
	if got := read("tasks/personal.md"); got != wantPersonal {
		t.Fatalf("tasks personal:\n--- got ---\n%s\n--- want ---\n%s", got, wantPersonal)
	}
	if got := read("tasks/work.md"); got != "# Việc — work\n\n- [ ] Họp nhóm\n" {
		t.Fatalf("tasks work: %q", got)
	}

	// relations + note nguồn
	wantRel := "# Liên quan\n\n- Viết tài liệu → SQLite local-first (mentions) — note #1\n"
	if got := read("relations.md"); got != wantRel {
		t.Fatalf("relations:\n--- got ---\n%s\n--- want ---\n%s", got, wantRel)
	}

	// idempotent: export lần 2 đè lên chính nó, không lỗi
	if _, err := New(st, nil, testCfg()).Export(ctx, out); err != nil {
		t.Fatalf("export lần 2: %v", err)
	}
}

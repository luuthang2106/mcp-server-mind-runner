package store

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestMigrateFromZero(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.Migrate(ctx, filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != LatestSchema {
		t.Fatalf("version=%d", v)
	}
	for _, tbl := range []string{
		"spaces", "sessions", "notes", "episodes", "session_raw", "tasks",
		"relations", "media", "chunks", "chunks_fts", "embeddings", "jobs",
		"meta", "schema_migrations",
	} {
		var n int
		if err := st.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = ?`, tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("thiếu bảng %s", tbl)
		}
	}
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM spaces`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("seed spaces=%d, muốn 3", n)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	bk := filepath.Join(t.TempDir(), "backups")
	if err := st.Migrate(ctx, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx, bk); err != nil {
		t.Fatal(err)
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != LatestSchema {
		t.Fatalf("version=%d", v)
	}
	matches, _ := filepath.Glob(filepath.Join(bk, "pre-migrate-*.db"))
	if len(matches) != 0 {
		t.Fatalf("backup rác khi không có migration mới: %v", matches)
	}
}

func TestFTSTriggers(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if err := st.Migrate(ctx, filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	res, err := st.DB().Exec(`INSERT INTO spaces(name, policy, created_at) VALUES ('t','cloud','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	spaceID, _ := res.LastInsertId()
	res, err = st.DB().Exec(`INSERT INTO notes(space_id, kind, text, source, content_hash, created_at, updated_at)
		VALUES (?, 'note', 'xin chào', 'test', 'h1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, spaceID)
	if err != nil {
		t.Fatal(err)
	}
	noteID, _ := res.LastInsertId()
	res, err = st.DB().Exec(`INSERT INTO chunks(note_id, ordinal, text, token_count) VALUES (?, 0, 'xin chào thế giới', 3)`, noteID)
	if err != nil {
		t.Fatal(err)
	}
	chunkID, _ := res.LastInsertId()

	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM chunks_fts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("chunks_fts=%d sau insert", n)
	}
	if _, err := st.DB().Exec(`DELETE FROM chunks WHERE id = ?`, chunkID); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM chunks_fts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("chunks_fts=%d sau delete (rác FTS)", n)
	}
}

func TestBackupBeforeMigrateOnExistingDB(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	bk := filepath.Join(t.TempDir(), "backups")
	if err := st.Migrate(ctx, bk); err != nil {
		t.Fatal(err)
	}

	realSQL, err := migrateFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	fake := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{Data: realSQL},
		"migrations/0099_test.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t2(x INTEGER);")},
	}
	if err := migrateWithFS(ctx, st.DB(), bk, fake); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(bk, "pre-migrate-*.db"))
	if len(matches) != 1 {
		t.Fatalf("backup pre-migrate: %v", matches)
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != 99 {
		t.Fatalf("version=%d", v)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='t2'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("0002 không được áp")
	}
}

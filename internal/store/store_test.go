package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// openMigrated: store với schema đầy đủ (migrate từ zero).
func openMigrated(t *testing.T) *Store {
	t.Helper()
	st := openTemp(t)
	if err := st.Migrate(context.Background(), filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestOpenPragmas(t *testing.T) {
	st := openTemp(t)
	var journal string
	if err := st.DB().QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode=%q", journal)
	}
	var fk int
	if err := st.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys=%d", fk)
	}
	var busy int
	if err := st.DB().QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != 5000 {
		t.Fatalf("busy_timeout=%d", busy)
	}
}

func TestForeignKeyEnforced(t *testing.T) {
	st := openTemp(t)
	if _, err := st.DB().Exec(`CREATE TABLE parent(id INTEGER PRIMARY KEY);
		CREATE TABLE child(id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent(id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO child(id, pid) VALUES (1, 99)`); err == nil {
		t.Fatal("insert con mồ côi phải lỗi FK")
	}
}

func TestTwoConnectionsWAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	st1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st1.Close()
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	if _, err := st1.DB().Exec(`CREATE TABLE kv(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := st1.DB().Exec(`INSERT INTO kv(k, v) VALUES ('a', '1') ON CONFLICT(k) DO UPDATE SET v=v||'x'`); err != nil {
			t.Fatalf("conn1 write %d: %v", i, err)
		}
		if _, err := st2.DB().Exec(`INSERT INTO kv(k, v) VALUES ('b', '1') ON CONFLICT(k) DO UPDATE SET v=v||'y'`); err != nil {
			t.Fatalf("conn2 write %d: %v", i, err)
		}
	}
}

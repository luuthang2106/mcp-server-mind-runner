package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrateFS embed.FS

// LatestSchema là version schema hiện hành (doctor so với giá trị này).
const LatestSchema = 7

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate áp các migration còn thiếu. Nếu DB đã có migration trước đó (DB cũ)
// thì VACUUM INTO backupDir trước khi áp file mới. An toàn khi nhiều process
// cùng start: check tồn tại ngoài tx (chỉ để quyết định backup), rồi
// BEGIN IMMEDIATE + re-check bên trong tx.
func (s *Store) Migrate(ctx context.Context, backupDir string) error {
	return migrateWithFS(ctx, s.db, backupDir, migrateFS)
}

// SchemaVersion trả version migration hiện tại.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func migrateWithFS(ctx context.Context, db *sql.DB, backupDir string, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("tạo schema_migrations: %w", err)
	}
	migs, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	applied, err := appliedSet(ctx, db)
	if err != nil {
		return err
	}
	pending := pickPending(migs, applied)
	if len(pending) == 0 {
		return nil
	}

	// DB cũ → backup trước khi áp file mới. VACUUM INTO không chạy được
	// trong transaction nên làm ngoài tx; đua process hiếm khi tạo 2 file — vô hại.
	if len(applied) > 0 {
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return fmt.Errorf("tạo backup dir: %w", err)
		}
		dst := uniqueBackupPath(backupDir, "pre-migrate-"+time.Now().UTC().Format("2006-01-02T15-04-05Z"))
		if _, err := db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(dst, "'", "''")+`'`); err != nil {
			// Process khác vừa backup cùng lúc (file đích đã tồn tại) → bản backup
			// đó đủ dùng; chỉ fail khi file đích thực sự không có.
			if _, statErr := os.Stat(dst); statErr != nil {
				return fmt.Errorf("backup pre-migrate: %w", err)
			}
		}
	}

	if _, err := db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migrate: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			db.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Re-check trong tx — process khác có thể vừa áp xong.
	applied, err = appliedSet(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range pickPending(pending, applied) {
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("migration %04d %s: %w", m.version, m.name, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("ghi schema_migrations %d: %w", m.version, err)
		}
	}
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit migrate: %w", err)
	}
	ok = true
	return nil
}

// uniqueBackupPath trả base.db, hoặc base-N.db nếu đã tồn tại (VACUUM INTO
// không ghi đè file có sẵn).
func uniqueBackupPath(dir, base string) string {
	p := filepath.Join(dir, base+".db")
	for i := 2; i < 100; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d.db", base, i))
	}
	return p
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("đọc migrations/: %w", err)
	}
	var migs []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		idx := strings.IndexByte(name, '_')
		if idx <= 0 {
			return nil, fmt.Errorf("tên migration không hợp lệ: %q", name)
		}
		v, err := strconv.Atoi(name[:idx])
		if err != nil {
			return nil, fmt.Errorf("tên migration không hợp lệ: %q", name)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("trùng version migration %d: %q và %q", v, prev, name)
		}
		seen[v] = name
		data, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, err
		}
		migs = append(migs, migration{version: v, name: name, sql: string(data)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

func appliedSet(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func pickPending(migs []migration, applied map[int]bool) []migration {
	var out []migration
	for _, m := range migs {
		if !applied[m.version] {
			out = append(out, m)
		}
	}
	return out
}

// Package store là tầng SQL thuần (không nghiệp vụ) trên SQLite.
package store

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db  *sql.DB
	gen atomic.Uint64
}

// Open mở SQLite tại path với pragmas chuẩn của dự án:
// WAL + busy_timeout 5000ms + foreign_keys ON + synchronous NORMAL.
// Pool 1 connection/process (SQLite single-writer; busy_timeout xử lý đa process).
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("mở sqlite %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB trả *sql.DB cho migrations/tests.
func (s *Store) DB() *sql.DB { return s.db }

// Gen là generation counter cho cache vector (M3): mọi hàm ghi của store
// gọi BumpGen() để báo cache cũ.
func (s *Store) Gen() uint64 { return s.gen.Load() }

func (s *Store) BumpGen() { s.gen.Add(1) }

// Quy ước timestamp: mọi cột TEXT thời gian = UTC, phần lẻ 9 chữ số CỐ ĐỊNH để
// so sánh chuỗi trong SQL (<, >=) đúng thứ tự thời gian. RFC3339Nano cắt số 0
// cuối ("…05Z" vs "…05.5Z") làm so sánh chuỗi sai. parseTS đọc được cả dữ
// liệu cũ (RFC3339Nano) lẫn định dạng mới.
const tsLayout = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// TS định dạng thời gian theo quy ước cột TEXT của store (cho SQL ở package khác).
func TS(t time.Time) string { return ts(t) }

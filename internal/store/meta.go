package store

import (
	"context"
	"database/sql"
	"strconv"
)

// GetMeta đọc meta key; ok=false khi chưa có (không phải lỗi).
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.DB().QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta ghi/đè meta key.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.DB().ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// IncrMeta cộng dồn bộ đếm usage (không BumpGen — meta không nằm trong cache).
func (s *Store) IncrMeta(ctx context.Context, key string, delta int) error {
	_, err := s.DB().ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = CAST(CAST(meta.value AS INTEGER) + CAST(excluded.value AS INTEGER) AS TEXT)`,
		key, strconv.Itoa(delta))
	return err
}

// SetMetaIfChanged ghi key=value và trả true nếu giá trị thực sự đổi (key
// mới hoặc khác giá trị cũ). Một câu upsert nguyên tử → dùng làm compare-and-set
// cho gate "1 lần/ngày" khi nhiều hook/process chạy cùng lúc.
func (s *Store) SetMetaIfChanged(ctx context.Context, key, value string) (bool, error) {
	res, err := s.DB().ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE meta.value != excluded.value`, key, value)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

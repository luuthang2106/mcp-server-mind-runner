package store

import "context"

// DataVersion trả PRAGMA data_version: đổi khi process KHÁC commit vào DB
// (maintenance, hook, CLI). Giá trị theo connection — pool 1 connection
// (SetMaxOpenConns(1)) nên so sánh giữa các lần gọi là nhất quán. Ghi của
// chính process này không đổi giá trị (đã có Gen()).
func (s *Store) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.DB().QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v)
	return v, err
}

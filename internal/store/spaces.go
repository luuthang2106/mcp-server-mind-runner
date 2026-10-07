package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Space một dòng bảng spaces. Policy ở đây là seed thông tin — nguồn chân lý
// để định tuyến là config ([spaces.policy]).
type Space struct {
	ID     int64
	Name   string
	Policy string
}

// SpaceByName tra id space theo tên.
func (s *Store) SpaceByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.DB().QueryRowContext(ctx, `SELECT id FROM spaces WHERE name=?`, name).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("space không tồn tại: %q", name)
	}
	return id, err
}

// SpaceIDOrList như SpaceByName nhưng khi sai tên thì liệt kê space hợp lệ
// (thông báo cho model/CLI tự sửa).
func (s *Store) SpaceIDOrList(ctx context.Context, name string) (int64, error) {
	id, err := s.SpaceByName(ctx, name)
	if err == nil {
		return id, nil
	}
	sps, lerr := s.Spaces(ctx)
	if lerr != nil {
		return 0, lerr
	}
	names := make([]string, len(sps))
	for i, sp := range sps {
		names[i] = sp.Name
	}
	return 0, fmt.Errorf("space không tồn tại: %q — space hợp lệ: %s", name, strings.Join(names, ", "))
}

// SpaceByID đọc một space theo id.
func (s *Store) SpaceByID(ctx context.Context, id int64) (Space, error) {
	var sp Space
	err := s.DB().QueryRowContext(ctx,
		`SELECT id, name, policy FROM spaces WHERE id=?`, id).
		Scan(&sp.ID, &sp.Name, &sp.Policy)
	if err == sql.ErrNoRows {
		return Space{}, fmt.Errorf("space không tồn tại: id=%d", id)
	}
	return sp, err
}

// Spaces trả tất cả space theo id (bảng nhỏ — cho hiển thị/định tuyến).
func (s *Store) Spaces(ctx context.Context) ([]Space, error) {
	rows, err := s.DB().QueryContext(ctx, `SELECT id, name, policy FROM spaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Space
	for rows.Next() {
		var sp Space
		if err := rows.Scan(&sp.ID, &sp.Name, &sp.Policy); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

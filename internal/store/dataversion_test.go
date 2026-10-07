package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestDataVersionSeesOtherConnection: ghi từ store khác (process khác) → đổi;
// ghi của chính store → không đổi.
func TestDataVersionSeesOtherConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "m.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, s := range []*Store{a, b} {
		if _, err := s.DB().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS t (x INTEGER)`); err != nil {
			t.Fatal(err)
		}
	}
	v0, err := a.DataVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB().ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if v1, _ := a.DataVersion(ctx); v1 != v0 {
		t.Fatalf("ghi cùng connection đổi data_version: %d → %d", v0, v1)
	}
	if _, err := b.DB().ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	if v2, _ := a.DataVersion(ctx); v2 == v0 {
		t.Fatalf("ghi từ connection khác không đổi data_version (%d)", v2)
	}
}

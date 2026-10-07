package store

import (
	"context"
	"testing"
)

// TestSpaceByIDReadsPolicy: đọc đủ id/name/policy seed; id lạ → lỗi rõ.
func TestSpaceByIDReadsPolicy(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	id, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	sp, err := st.SpaceByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if sp.ID != id || sp.Name != "personal" || sp.Policy != "cloud" {
		t.Fatalf("sp=%+v", sp)
	}
	if _, err := st.SpaceByID(ctx, 9999); err == nil {
		t.Fatal("id không tồn tại phải lỗi")
	}
}

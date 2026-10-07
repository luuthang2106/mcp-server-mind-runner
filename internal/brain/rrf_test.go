package brain

import (
	"math"
	"testing"
)

// TestFuseScoresAndOrder: điểm RRF đúng công thức Σ 1/(RRFK+rank+1), sắp score desc.
func TestFuseScoresAndOrder(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-12 }

	fused := Fuse([]int64{101, 102, 103}, []int64{102, 104})
	if len(fused) != 4 {
		t.Fatalf("len=%d, muốn 4: %+v", len(fused), fused)
	}
	want := []struct {
		id    int64
		score float64
	}{
		{102, 1.0/(RRFK+1) + 1.0/(RRFK+2)}, // rank1 list1 + rank0 list2
		{101, 1.0 / (RRFK + 1)},
		{104, 1.0 / (RRFK + 2)},
		{103, 1.0 / (RRFK + 3)},
	}
	for i, w := range want {
		if fused[i].ID != w.id || !near(fused[i].Score, w.score) {
			t.Fatalf("fused[%d]=%+v, muốn id=%d score=%v", i, fused[i], w.id, w.score)
		}
	}
}

// TestFuseTieBreaksByIDAsc: điểm bằng nhau → ID nhỏ trước (tất định).
func TestFuseTieBreaksByIDAsc(t *testing.T) {
	fused := Fuse([]int64{5}, []int64{4})
	if len(fused) != 2 || fused[0].ID != 4 || fused[1].ID != 5 {
		t.Fatalf("fused=%+v, muốn [4 5]", fused)
	}
}

// TestFuseEmpty: danh sách rỗng/nil không lỗi, trả rỗng.
func TestFuseEmpty(t *testing.T) {
	if got := Fuse(); len(got) != 0 {
		t.Fatalf("Fuse()=%v", got)
	}
	if got := Fuse(nil, []int64{}); len(got) != 0 {
		t.Fatalf("Fuse rỗng=%v", got)
	}
}

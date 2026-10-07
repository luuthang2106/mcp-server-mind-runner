package brain

import (
	"container/heap"
	"context"
	"math"
	"sort"

	"mind-runner/internal/store"
)

// Cosine: cosin giữa hai vector cùng chiều; vector 0 → 0 (không NaN).
func Cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// VectorHit: chunk khớp vector; sắp score desc, tie chunk_id asc.
type VectorHit struct {
	ChunkID, NoteID int64
	Score           float64
}

// better: a xếp trước b (score cao hơn; bằng → chunk_id nhỏ hơn).
func better(a, b VectorHit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.ChunkID < b.ChunkID
}

// hitHeap: min-heap theo better — gốc là hit tệ nhất trong top-n đang giữ.
type hitHeap []VectorHit

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return better(h[j], h[i]) }
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(x any)        { *h = append(*h, x.(VectorHit)) }
func (h *hitHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// VectorTop: top-n chunk gần q nhất (cosine) trong note khớp filter. Quét
// thẳng embeddings trong SQLite (vector int8) và chỉ giữ top-n — RAM không
// tăng theo số tài liệu, nhiều process (mỗi app một process) không nhân bản
// cache; trang đĩa nằm trong page cache của OS, dùng chung. Bỏ row sai chiều;
// n<=0 = không cắt.
func (b *Brain) VectorTop(ctx context.Context, model string, f store.NoteFilter, q []float32, n int) ([]VectorHit, error) {
	qn := normalize(q)
	var h hitHeap
	err := b.st.ScanVectors(ctx, model, f, func(r *store.VecRow) error {
		score, ok := r.Dot(qn)
		if !ok {
			return nil
		}
		hit := VectorHit{ChunkID: r.ChunkID, NoteID: r.NoteID, Score: score}
		if n <= 0 || h.Len() < n {
			heap.Push(&h, hit)
		} else if better(hit, h[0]) {
			h[0] = hit
			heap.Fix(&h, 0)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	hits := []VectorHit(h)
	sort.Slice(hits, func(i, j int) bool { return better(hits[i], hits[j]) })
	return hits, nil
}

// normalize: bản sao chuẩn hoá L2 (vector 0 giữ nguyên → điểm 0).
func normalize(v []float32) []float32 {
	var n2 float64
	for _, x := range v {
		n2 += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	if n2 == 0 {
		return out
	}
	inv := 1 / math.Sqrt(n2)
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

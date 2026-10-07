package brain

import "sort"

// RRFK là hằng số làm phẳng Reciprocal Rank Fusion (Cormack et al., k=60).
const RRFK = 60

// Fused một ID sau hợp nhất kèm điểm RRF tổng.
type Fused struct {
	ID    int64
	Score float64
}

// Fuse hợp nhất các danh sách xếp hạng bằng RRF: rank 0-based, mỗi list đóng
// góp 1/(RRFK+rank+1) cho ID của nó. Kết quả sắp score desc, tie ID asc
// (tất định — không phụ thuộc thứ tự list).
func Fuse(lists ...[]int64) []Fused {
	score := map[int64]float64{}
	for _, list := range lists {
		for rank, id := range list {
			score[id] += 1 / float64(RRFK+rank+1)
		}
	}
	out := make([]Fused, 0, len(score))
	for id, s := range score {
		out = append(out, Fused{ID: id, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

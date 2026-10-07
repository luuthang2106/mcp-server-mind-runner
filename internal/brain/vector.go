package brain

import (
	"context"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

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

// vecCache: ảnh chụp embeddings theo (model, spaceIDs, tags) tại generation gen
// + data_version dv (ghi từ process khác).
type vecCache struct {
	gen  uint64
	dv   int64
	key  string
	rows []store.VecRow
}

func vecKey(model string, spaceIDs []int64, tags []string) string {
	ids := slices.Clone(spaceIDs)
	slices.Sort(ids)
	var sb strings.Builder
	sb.WriteString(model)
	for _, id := range ids {
		sb.WriteByte(':')
		sb.WriteString(strconv.FormatInt(id, 10))
	}
	ts := slices.Clone(tags)
	slices.Sort(ts)
	for _, t := range ts {
		sb.WriteString("#")
		sb.WriteString(strconv.Quote(t))
	}
	return sb.String()
}

// vectors: embeddings theo (model, spaceIDs, tags), cache theo generation (ghi
// trong process — BumpGen M1.3) + PRAGMA data_version (ghi của process khác:
// maintenance, hook, CLI) — một trong hai đổi → đọc lại.
func (b *Brain) vectors(ctx context.Context, model string, spaceIDs []int64, tags []string) ([]store.VecRow, error) {
	key := vecKey(model, spaceIDs, tags)
	b.mu.Lock()
	defer b.mu.Unlock()
	// đọc TRƯỚC query: ghi chen giữa query và đây → gen/dv mới → cache coi như cũ
	gen := b.st.Gen()
	dv, err := b.st.DataVersion(ctx)
	if err != nil {
		return nil, err
	}
	if c := b.cache; c != nil && c.gen == gen && c.dv == dv && c.key == key {
		return c.rows, nil
	}
	rows, err := b.st.VectorsForSpaces(ctx, model,
		store.NoteFilter{SpaceIDs: spaceIDs, Tags: tags, IncludeSuperseded: true})
	if err != nil {
		return nil, err
	}
	b.cache = &vecCache{gen: gen, dv: dv, key: key, rows: rows}
	return rows, nil
}

// VectorHit: chunk khớp vector; sắp score desc, tie chunk_id asc.
type VectorHit struct {
	ChunkID, NoteID int64
	Score           float64
}

// VectorTop: top-n chunk gần q nhất (cosine) trong note khớp filter. Cache
// theo (model, spaces, tags); kind/status lọc trong bộ nhớ để đổi kinds giữa
// các recall không phải đọc lại toàn bộ embeddings. Bỏ row sai chiều; n<=0 = không cắt.
func (b *Brain) VectorTop(ctx context.Context, model string, f store.NoteFilter, q []float32, n int) ([]VectorHit, error) {
	rows, err := b.vectors(ctx, model, f.SpaceIDs, f.Tags)
	if err != nil {
		return nil, err
	}
	kinds := map[string]bool{}
	for _, k := range f.Kinds {
		kinds[k] = true
	}
	hits := make([]VectorHit, 0, len(rows))
	for _, r := range rows {
		if len(r.Vec) != len(q) {
			continue
		}
		if !f.IncludeSuperseded && r.Status != "active" {
			continue
		}
		if len(kinds) > 0 && !kinds[r.Kind] {
			continue
		}
		hits = append(hits, VectorHit{ChunkID: r.ChunkID, NoteID: r.NoteID, Score: Cosine(q, r.Vec)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ChunkID < hits[j].ChunkID
	})
	if n > 0 && len(hits) > n {
		hits = hits[:n]
	}
	return hits, nil
}

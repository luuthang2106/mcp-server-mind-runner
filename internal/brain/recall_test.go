package brain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

// seedRecallNote viết note + embed ngay (đường vector sẵn sàng).
func seedRecallNote(t *testing.T, b *Brain, ctx context.Context, spaceID int64, text string) int64 {
	t.Helper()
	res, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: text, Source: "tool:remember"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EmbedNote(ctx, res.NoteID); err != nil {
		t.Fatal(err)
	}
	return res.NoteID
}

// TestRecallUnionFTSAndVector: A khớp chỉ FTS, B khớp chỉ vector → union qua RRF,
// thứ tự theo điểm RRF, stages báo đủ 3 tầng.
func TestRecallUnionFTSAndVector(t *testing.T) {
	st := newStore(t)
	vecByText := map[string][]float32{
		"alpha":        {1, 0, 0}, // vector của query
		"dự án alpha":  {0, 1, 0}, // xa query → chỉ khớp FTS
		"ghi chú beta": {1, 0, 0}, // khớp vector, không có token "alpha"
	}
	fake := egressfake.New(t, egressfake.Options{
		EmbedVec: func(text string) []float32 { return vecByText[text] },
	})
	b := New(st, fake.Egress, testCfg())
	ctx := context.Background()
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}

	idA := seedRecallNote(t, b, ctx, spaceID, "dự án alpha")
	idB := seedRecallNote(t, b, ctx, spaceID, "ghi chú beta")

	res, err := b.Recall(ctx, RecallParams{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	// A: 1/61 (fts rank0) + 1/62 (vec rank1); B: 1/61 (vec rank0) → A trước
	if len(res.Hits) != 2 || res.Hits[0].NoteID != idA || res.Hits[1].NoteID != idB {
		t.Fatalf("hits=%+v, muốn [%d %d]", res.Hits, idA, idB)
	}
	if res.Stages["fts"] != "ok:1" {
		t.Fatalf("stages[fts]=%q", res.Stages["fts"])
	}
	if res.Stages["vector"] != "ok:2" {
		t.Fatalf("stages[vector]=%q", res.Stages["vector"])
	}
	if !strings.HasPrefix(res.Stages["rerank"], "ok:") {
		t.Fatalf("stages[rerank]=%q", res.Stages["rerank"])
	}
	// metadata đủ cho hiển thị
	h := res.Hits[0]
	if h.Kind != "note" || h.Space != "personal" || h.Source != "tool:remember" || h.Text == "" || h.ChunkID <= 0 {
		t.Fatalf("hit=%+v", h)
	}
}

// TestRecallRerankErrorKeepsRRFOrder: rerank lỗi → stages "skipped", giữ thứ tự
// RRF và score = RRF (không phải rerank).
func TestRecallRerankErrorKeepsRRFOrder(t *testing.T) {
	st := newStore(t)
	vecByText := map[string][]float32{
		"alpha":        {1, 0, 0},
		"dự án alpha":  {0, 1, 0},
		"ghi chú beta": {1, 0, 0},
	}
	fake := egressfake.New(t, egressfake.Options{
		EmbedVec:  func(text string) []float32 { return vecByText[text] },
		RerankErr: errors.New("gateway rerank hỏng"),
	})
	b := New(st, fake.Egress, testCfg())
	ctx := context.Background()
	spaceID, _ := st.SpaceByName(ctx, "personal")

	idA := seedRecallNote(t, b, ctx, spaceID, "dự án alpha")
	idB := seedRecallNote(t, b, ctx, spaceID, "ghi chú beta")

	res, err := b.Recall(ctx, RecallParams{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Stages["rerank"], "skipped:") {
		t.Fatalf("stages[rerank]=%q", res.Stages["rerank"])
	}
	if len(res.Hits) != 2 || res.Hits[0].NoteID != idA || res.Hits[1].NoteID != idB {
		t.Fatalf("hits=%+v, muốn giữ thứ tự RRF [%d %d]", res.Hits, idA, idB)
	}
	if res.Hits[0].Score <= res.Hits[1].Score {
		t.Fatalf("score phải là RRF giảm dần: %v %v", res.Hits[0].Score, res.Hits[1].Score)
	}
}

// TestRecallEmbedErrorDegradesNoEnqueue: embed lỗi → FTS-only, stages vector
// "error", và KHÔNG enqueue embed_chunk dù gọi lặp.
func TestRecallEmbedErrorDegradesNoEnqueue(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{
		EmbedErr: errors.New("gateway embed hỏng"),
	})
	b := New(st, fake.Egress, testCfg())
	ctx := context.Background()
	spaceID, _ := st.SpaceByName(ctx, "personal")

	// 2 note chưa embed (worker chưa chạy) — cả hai thiếu embedding
	res1, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: "dự án alpha", Source: "tool:remember"})
	if err != nil {
		t.Fatal(err)
	}
	res2, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: "ghi chú thầm lặng", Source: "tool:remember"})
	if err != nil {
		t.Fatal(err)
	}
	// dọn job của WriteNote để tách enqueue backfill của recall
	if _, err := st.DB().ExecContext(ctx, `UPDATE jobs SET state='done'`); err != nil {
		t.Fatal(err)
	}

	res, err := b.Recall(ctx, RecallParams{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Stages["vector"], "error:") {
		t.Fatalf("stages[vector]=%q", res.Stages["vector"])
	}
	if len(res.Hits) != 1 || res.Hits[0].NoteID != res1.NoteID {
		t.Fatalf("hits=%+v, muốn FTS-only [%d]", res.Hits, res1.NoteID)
	}

	// recall KHÔNG enqueue backfill (WriteNote + maintenance lo) — tránh job trùng vô hạn
	for i := 0; i < 3; i++ {
		if _, err := b.Recall(ctx, RecallParams{Query: "alpha"}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE type='embed_chunk' AND state='queued'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("recall đã enqueue %d job embed_chunk, muốn 0", n)
	}
	_ = res2
}

// TestRecallNoteOnceWithMultipleMatchingChunks: 2 chunk cùng note đều khớp →
// note chỉ xuất hiện 1 lần, giữ chunk hạng tốt nhất.
func TestRecallNoteOnceWithMultipleMatchingChunks(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{})
	b := New(st, fake.Egress, testCfg())
	ctx := context.Background()
	spaceID, _ := st.SpaceByName(ctx, "personal")

	id := seedRecallNote(t, b, ctx, spaceID, "alpha đoạn một\n\nđoạn hai cũng alpha")

	// tiền đề: 2 chunk của cùng note đều khớp FTS
	hits, err := st.SearchFTS(ctx, "alpha", store.NoteFilter{}, 50)
	if err != nil || len(hits) != 2 || hits[0].NoteID != id || hits[1].NoteID != id {
		t.Fatalf("premise FTS: hits=%+v err=%v", hits, err)
	}

	res, err := b.Recall(ctx, RecallParams{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].NoteID != id {
		t.Fatalf("hits=%+v, muốn đúng 1 hit cho note %d", res.Hits, id)
	}
	if !strings.Contains(res.Hits[0].Text, "alpha") {
		t.Fatalf("text=%q", res.Hits[0].Text)
	}
}

// TestRecallLimit: Limit cắt kết quả; Limit 0 → mặc định 8.
func TestRecallLimit(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{})
	b := New(st, fake.Egress, testCfg())
	ctx := context.Background()
	spaceID, _ := st.SpaceByName(ctx, "personal")

	for _, text := range []string{"alpha một", "alpha hai", "alpha ba"} {
		seedRecallNote(t, b, ctx, spaceID, text)
	}
	res, err := b.Recall(ctx, RecallParams{Query: "alpha", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("hits=%d, muốn 2", len(res.Hits))
	}
}

// TestRecallLocalDeadNeverFallsBackToCloud (spec verify): space `work` policy
// local; local chết → vector stage "error", FTS vẫn chạy (SQLite local),
// cloud nhận 0 request ở mọi endpoint — không có fallback ngầm.
func TestRecallLocalDeadNeverFallsBackToCloud(t *testing.T) {
	st := newStore(t)
	cloud := egressfake.New(t, egressfake.Options{})
	local := egressfake.NewLocal(t, egressfake.Options{})
	cfg := egressfake.Config(cloud, local)
	cfg.Spaces.Policy = map[string]string{"work": "local"}
	eg := egress.New(cfg, nil)
	b := New(st, eg, &cfg)
	ctx := context.Background()
	spaceID, err := st.SpaceByName(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}

	// seed + embed trong lúc local còn sống
	id := seedRecallNote(t, b, ctx, spaceID, "dự án alpha")
	local.Close() // endpoint local chết

	res, err := b.Recall(ctx, RecallParams{Query: "alpha", SpaceIDs: []int64{spaceID}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Stages["vector"], "error:") {
		t.Fatalf("stages[vector]=%q, muốn error", res.Stages["vector"])
	}
	// FTS không cần egress → note vẫn hiện
	if len(res.Hits) != 1 || res.Hits[0].NoteID != id {
		t.Fatalf("hits=%+v, muốn FTS-only [%d]", res.Hits, id)
	}
	if !strings.HasPrefix(res.Stages["rerank"], "skipped:") {
		t.Fatalf("stages[rerank]=%q, muốn skipped (local chết)", res.Stages["rerank"])
	}
	if cloud.EmbedCalls != 0 || cloud.RerankCalls != 0 || cloud.ChatCalls != 0 {
		t.Fatalf("cloud bị gọi: embed=%d rerank=%d chat=%d, muốn 0/0/0",
			cloud.EmbedCalls, cloud.RerankCalls, cloud.ChatCalls)
	}
}

// TestRecallMixedPolicy (spec verify 4.3): 2 space khác policy + 2 fake → hit
// cả hai, vector "ok", rerank "skipped: mixed policy" (không trộn nguồn docs);
// local chết → chỉ còn hit phía cloud, vector "partial".
func TestRecallMixedPolicy(t *testing.T) {
	st := newStore(t)
	vecByText := map[string][]float32{
		"alpha":                 {1, 0, 0}, // embedding query (cả hai fakes)
		"dự án alpha công khai": {0, 1, 0}, // xa query → chỉ khớp FTS (cloud)
		"ghi chú beta nội bộ":   {1, 0, 0}, // chỉ khớp vector (local)
	}
	emb := func(text string) []float32 { return vecByText[text] }
	cloud := egressfake.New(t, egressfake.Options{EmbedVec: emb})
	local := egressfake.NewLocal(t, egressfake.Options{EmbedVec: emb})
	cfg := egressfake.Config(cloud, local)
	cfg.Spaces.Policy = map[string]string{"work": "local"}
	eg := egress.New(cfg, nil)
	b := New(st, eg, &cfg)
	ctx := context.Background()
	personalID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	workID, err := st.SpaceByName(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}

	idA := seedRecallNote(t, b, ctx, personalID, "dự án alpha công khai")
	idB := seedRecallNote(t, b, ctx, workID, "ghi chú beta nội bộ")

	res, err := b.Recall(ctx, RecallParams{Query: "alpha", SpaceIDs: []int64{personalID, workID}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, h := range res.Hits {
		got[h.NoteID] = true
	}
	if !got[idA] || !got[idB] {
		t.Fatalf("hits=%+v, muốn cả %d (cloud) và %d (local)", res.Hits, idA, idB)
	}
	if res.Stages["rerank"] != "skipped: mixed policy" {
		t.Fatalf("stages[rerank]=%q", res.Stages["rerank"])
	}
	if !strings.HasPrefix(res.Stages["vector"], "ok:") {
		t.Fatalf("stages[vector]=%q", res.Stages["vector"])
	}
	if cloud.RerankCalls != 0 || local.RerankCalls != 0 {
		t.Fatalf("rerank phải không gọi khi mixed: cloud=%d local=%d", cloud.RerankCalls, local.RerankCalls)
	}

	// local chết → B (vốn chỉ vào bằng vector) mất, A còn qua FTS
	local.Close()
	res, err = b.Recall(ctx, RecallParams{Query: "alpha", SpaceIDs: []int64{personalID, workID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].NoteID != idA {
		t.Fatalf("hits=%+v, muốn chỉ [%d]", res.Hits, idA)
	}
	if !strings.HasPrefix(res.Stages["vector"], "partial:") {
		t.Fatalf("stages[vector]=%q, muốn partial", res.Stages["vector"])
	}
}

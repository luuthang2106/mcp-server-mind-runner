package brain

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"

	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

func TestCosine(t *testing.T) {
	a := []float32{1, 0, 0}
	closeTo := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	if got := Cosine(a, a); !closeTo(got, 1) {
		t.Fatalf("Cosine(a,a)=%v", got)
	}
	if got := Cosine(a, []float32{0, 1, 0}); !closeTo(got, 0) {
		t.Fatalf("Cosine vuông góc=%v", got)
	}
	if got := Cosine(a, []float32{-1, 0, 0}); !closeTo(got, -1) {
		t.Fatalf("Cosine ngược=%v", got)
	}
	if got := Cosine(a, []float32{0, 0, 0}); got != 0 {
		t.Fatalf("Cosine zero=%v, muốn 0 (không NaN)", got)
	}
}

// TestVectorTopOrderTiebreakCache: thứ tự score desc + tie chunk_id asc;
// cache theo generation — ghi thô không BumpGen → còn dữ liệu cũ; BumpGen → mới.
func TestVectorTopOrderTiebreakCache(t *testing.T) {
	st := newStore(t)
	vecByText := map[string][]float32{
		"gần":  {1, 0, 0},
		"giữa": {0.7, 0.7, 0},
		"xa":   {0, 1, 0},
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

	var ids []int64
	for _, text := range []string{"gần", "xa", "giữa"} {
		res, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: text, Source: "tool:remember"})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.EmbedNote(ctx, res.NoteID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.NoteID)
	}

	model := fake.Egress.EmbedModel(egress.PolicyCloud)
	hits, err := b.VectorTop(ctx, model, store.NoteFilter{}, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 || hits[0].NoteID != ids[0] || hits[1].NoteID != ids[2] || hits[2].NoteID != ids[1] {
		t.Fatalf("hits=%+v, muốn [%d %d %d]", hits, ids[0], ids[2], ids[1])
	}
	if math.Abs(hits[0].Score-1) > 1e-9 || hits[1].Score <= hits[2].Score {
		t.Fatalf("scores=%v", hits)
	}

	// tie-break: note mới cùng vec với "gần" → cùng score 1, chunk_id nhỏ trước
	vecByText["gần2"] = []float32{1, 0, 0}
	res2, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: "gần2", Source: "tool:remember"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EmbedNote(ctx, res2.NoteID); err != nil {
		t.Fatal(err)
	}
	hits, err = b.VectorTop(ctx, model, store.NoteFilter{}, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 || hits[0].NoteID != ids[0] || hits[1].NoteID != res2.NoteID {
		t.Fatalf("tie-break hits=%+v, muốn [%d %d ...]", hits, ids[0], res2.NoteID)
	}

	// cache: ghi thô KHÔNG BumpGen → VectorTop vẫn trả tập cũ
	rawInsertNote(t, st, spaceID, "gần3", []float32{1, 0, 0}, model)
	hits, err = b.VectorTop(ctx, model, store.NoteFilter{}, []float32{1, 0, 0}, 10)
	if err != nil || len(hits) != 4 {
		t.Fatalf("cache: hits=%d err=%v, muốn còn 4 (chưa BumpGen)", len(hits), err)
	}
	st.BumpGen()
	hits, err = b.VectorTop(ctx, model, store.NoteFilter{}, []float32{1, 0, 0}, 10)
	if err != nil || len(hits) != 5 {
		t.Fatalf("sau BumpGen: hits=%d err=%v, muốn 5", len(hits), err)
	}
}

// rawInsertNote ghi note+chunk+embedding bằng SQL thô (không qua store methods
// → không BumpGen), phục vụ test cache.
func rawInsertNote(t *testing.T, st *store.Store, spaceID int64, text string, vec []float32, model string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := st.DB().ExecContext(ctx,
		`INSERT INTO notes(space_id, kind, text, tags, source, content_hash, created_at, updated_at)
		 VALUES(?, 'note', ?, '[]', 'test:raw', ?, ?, ?)`,
		spaceID, text, fmt.Sprintf("%x", sha256.Sum256([]byte(text))), now, now)
	if err != nil {
		t.Fatal(err)
	}
	noteID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	res, err = st.DB().ExecContext(ctx,
		`INSERT INTO chunks(note_id, ordinal, text, token_count) VALUES(?, 0, ?, ?)`,
		noteID, text, (len(text)+3)/4)
	if err != nil {
		t.Fatal(err)
	}
	chunkID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(f))
	}
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO embeddings(chunk_id, model, dim, vec, created_at) VALUES(?,?,?,?,?)`,
		chunkID, model, len(vec), raw, now); err != nil {
		t.Fatal(err)
	}
}

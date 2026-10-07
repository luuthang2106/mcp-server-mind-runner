package brain

import (
	"context"
	"path/filepath"
	"testing"

	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

// TestVectorCacheSeesOtherProcess: ghi từ store khác cùng file (process khác:
// maintenance/hook) → cache vô hiệu qua data_version dù Gen không đổi.
func TestVectorCacheSeesOtherProcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx, filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	fake := egressfake.New(t, egressfake.Options{EmbedVec: func(string) []float32 { return []float32{1, 0, 0} }})
	b := New(st, fake.Egress, testCfg())
	spaceID, _ := st.SpaceByName(ctx, "personal")
	model := fake.Egress.EmbedModel(egress.PolicyCloud)

	res, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: "một", Source: "tool:remember"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EmbedNote(ctx, res.NoteID); err != nil {
		t.Fatal(err)
	}
	if hits, err := b.VectorTop(ctx, model, store.NoteFilter{}, []float32{1, 0, 0}, 10); err != nil || len(hits) != 1 {
		t.Fatalf("hits=%d err=%v", len(hits), err)
	}
	gen := st.Gen()
	rawInsertNote(t, other, spaceID, "hai", []float32{1, 0, 0}, model)
	hits, err := b.VectorTop(ctx, model, store.NoteFilter{}, []float32{1, 0, 0}, 10)
	if err != nil || len(hits) != 2 || st.Gen() != gen {
		t.Fatalf("hits=%d err=%v, muốn 2 (ghi process khác)", len(hits), err)
	}
}

// TestVectorTopTagFilter: vector side lọc tags như FTS → note không tag không lọt.
func TestVectorTopTagFilter(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{EmbedVec: func(string) []float32 { return []float32{1, 0, 0} }})
	b := New(st, fake.Egress, testCfg())
	spaceID, _ := st.SpaceByName(ctx, "personal")
	model := fake.Egress.EmbedModel(egress.PolicyCloud)

	var tagged int64
	for i, tags := range [][]string{{"work"}, nil} {
		res, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: []string{"có tag", "không tag"}[i], Tags: tags, Source: "tool:remember"})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.EmbedNote(ctx, res.NoteID); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			tagged = res.NoteID
		}
	}
	hits, err := b.VectorTop(ctx, model, store.NoteFilter{Tags: []string{"work"}}, []float32{1, 0, 0}, 10)
	if err != nil || len(hits) != 1 || hits[0].NoteID != tagged {
		t.Fatalf("hits=%+v err=%v, muốn chỉ note %d", hits, err, tagged)
	}
	// cache theo tags: không tag → cả hai
	if hits, _ := b.VectorTop(ctx, model, store.NoteFilter{}, []float32{1, 0, 0}, 10); len(hits) != 2 {
		t.Fatalf("không lọc: hits=%d, muốn 2", len(hits))
	}
}

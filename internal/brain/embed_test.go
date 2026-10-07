package brain

import (
	"context"
	"strings"
	"testing"

	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
)

// TestEmbedNoteEmbedsAllChunksOnce: WriteNote → EmbedNote phủ đủ embedding
// cho mọi chunk; gọi lần 2 không gọi egress lại (no-op).
func TestEmbedNoteEmbedsAllChunksOnce(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{})
	b := New(st, fake.Egress, testCfg())
	ctx := context.Background()
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("câu ví dụ dài cho chunker. ", 300) // ~7500 rune → ~8 chunk ≤10 → 1 batch
	res, err := b.WriteNote(ctx, WriteParams{
		SpaceID: spaceID, Kind: "note", Text: long, Source: "tool:remember",
	})
	if err != nil || !res.Fresh {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	chunks, err := st.ChunksOfNote(ctx, res.NoteID)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("chunks=%d err=%v, cần ≥2 để test", len(chunks), err)
	}

	if err := b.EmbedNote(ctx, res.NoteID); err != nil {
		t.Fatal(err)
	}
	missing, err := st.ChunksMissingEmbeddings(ctx, res.NoteID, fake.Egress.EmbedModel(egress.PolicyCloud))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing=%d err=%v", len(missing), err)
	}
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embeddings e JOIN chunks c ON c.id=e.chunk_id WHERE c.note_id=? AND e.model=?`,
		res.NoteID, fake.Egress.EmbedModel(egress.PolicyCloud)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(chunks) {
		t.Fatalf("embeddings=%d, muốn %d", n, len(chunks))
	}
	if fake.EmbedCalls != 1 {
		t.Fatalf("EmbedCalls=%d, muốn 1 (mọi chunk 1 batch)", fake.EmbedCalls)
	}

	// lần 2 → no-op
	if err := b.EmbedNote(ctx, res.NoteID); err != nil {
		t.Fatal(err)
	}
	if fake.EmbedCalls != 1 {
		t.Fatalf("EmbedCalls=%d sau lần 2, muốn 1", fake.EmbedCalls)
	}
}

// TestEmbedNoteLocalPolicyNeverTouchesCloud (spec verify): space `work` policy
// local → EmbedNote đi endpoint local, cloud nhận 0 request; embedding lưu
// theo model local (model cloud không bao giờ được dùng cho space này).
func TestEmbedNoteLocalPolicyNeverTouchesCloud(t *testing.T) {
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

	res, err := b.WriteNote(ctx, WriteParams{
		SpaceID: spaceID, Kind: "note", Text: "bí mật công việc", Source: "tool:remember",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EmbedNote(ctx, res.NoteID); err != nil {
		t.Fatal(err)
	}
	if cloud.EmbedCalls != 0 {
		t.Fatalf("cloud EmbedCalls=%d, muốn 0 (policy local tuyệt đối không rơi cloud)", cloud.EmbedCalls)
	}
	if local.EmbedCalls != 1 {
		t.Fatalf("local EmbedCalls=%d, muốn 1", local.EmbedCalls)
	}
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embeddings e JOIN chunks c ON c.id=e.chunk_id WHERE c.note_id=? AND e.model=?`,
		res.NoteID, "fake-local-embed").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("embedding phải được lưu với model local")
	}
}

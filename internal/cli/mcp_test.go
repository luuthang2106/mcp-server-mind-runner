package cli

import (
	"context"
	"path/filepath"
	"testing"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/execx"
	"mind-runner/internal/store"
	"mind-runner/internal/worker"
)

// TestSweepOnceProcessesQueuedJob: note → WriteNote enqueue embed_chunk →
// sweepOnce xử lý 1 job → embedding đủ.
func TestSweepOnceProcessesQueuedJob(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx, filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}

	fake := egressfake.New(t, egressfake.Options{})
	cfg := egressfake.Config(fake, nil)
	b := brain.New(st, fake.Egress, &cfg)
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.WriteNote(ctx, brain.WriteParams{
		SpaceID: spaceID, Kind: "note", Text: "ghi nhớ ngắn", Source: "tool:remember",
	})
	if err != nil {
		t.Fatal(err)
	}

	n, err := sweepOnce(ctx, st, worker.Deps{Store: st, Brain: b, Egress: fake.Egress, Config: &cfg,
		Execx: execx.OS{}, DataDir: t.TempDir()})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, muốn 1", n, err)
	}
	counts, err := st.JobCounts(ctx)
	if err != nil || counts["done"] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
	missing, err := st.ChunksMissingEmbeddings(ctx, res.NoteID, fake.Egress.EmbedModel(egress.PolicyCloud))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing=%d err=%v", len(missing), err)
	}
}

// TestSweepOnceLocalDeadJobFailsStaysLocal (spec verify): space policy local,
// local chết → job embed_chunk fail (attempts tăng, backoff → lần sweep sau
// thử lại); cloud nhận 0 request — không rơi cloud khi endpoint local hỏng.
func TestSweepOnceLocalDeadJobFailsStaysLocal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx, filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}

	cloud := egressfake.New(t, egressfake.Options{})
	local := egressfake.NewLocal(t, egressfake.Options{})
	cfg := egressfake.Config(cloud, local)
	cfg.Spaces.Policy = map[string]string{"personal": "local"}
	eg := egress.New(cfg, nil)
	b := brain.New(st, eg, &cfg)
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteNote(ctx, brain.WriteParams{
		SpaceID: spaceID, Kind: "note", Text: "ghi chú local", Source: "tool:remember",
	}); err != nil {
		t.Fatal(err)
	}

	local.Close() // endpoint local chết trước khi sweep

	n, err := sweepOnce(ctx, st, worker.Deps{Store: st, Brain: b, Egress: eg, Config: &cfg,
		Execx: execx.OS{}, DataDir: t.TempDir()})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, muốn 1 job được thử", n, err)
	}
	var state, lastErr string
	var attempts int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT state, attempts, COALESCE(last_error,'') FROM jobs WHERE type='embed_chunk'`).
		Scan(&state, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != 1 {
		t.Fatalf("state=%s attempts=%d last_error=%q, muốn failed/1 (retry được)", state, attempts, lastErr)
	}
	if cloud.EmbedCalls != 0 {
		t.Fatalf("cloud EmbedCalls=%d, muốn 0", cloud.EmbedCalls)
	}
}

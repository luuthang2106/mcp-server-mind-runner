package worker_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/media"
	"mind-runner/internal/store"
	"mind-runner/internal/worker"
)

// wroteRunner: fake execx.Runner luôn "sinh" file out (8MB — base64 > trần 10MiB)
// ở đối số cuối → mọi mức ladder đều vượt trần.
type wroteRunner struct{ out []byte }

func (w wroteRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	return nil, os.WriteFile(args[len(args)-1], w.out, 0o600)
}

// TestTranscribeMediaPermanentMarksDeadOneShot: job transcribe_media gặp lỗi
// vĩnh viễn (audio vượt trần, ladder cạn) → dead sau 1 lần chạy, không chờ đủ
// MaxAttempts; media row hiện dead.
func TestTranscribeMediaPermanentMarksDeadOneShot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}

	fake := egressfake.New(t, egressfake.Options{})
	cfg := egressfake.Config(fake, nil)
	cfg.DataDir = dir
	b := brain.New(st, fake.Egress, &cfg)
	fr := wroteRunner{out: bytes.Repeat([]byte{7}, 8_000_000)}
	md := media.New(st, b, fake.Egress, &cfg, fr, dir)

	spID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "dài.m4a")
	if err := os.WriteFile(src, bytes.Repeat([]byte{7}, 7_900_000), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatal(err)
	}

	reg := worker.NewRegistry()
	worker.RegisterAll(reg, worker.Deps{Store: st, Brain: b, Egress: fake.Egress, Config: &cfg,
		Execx: fr, DataDir: dir})
	if n, err := worker.Run(ctx, st, reg, 0, time.Now); err != nil || n != 1 {
		t.Fatalf("run: n=%d err=%v", n, err)
	}

	var state, lastErr string
	var attempts int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT state, attempts, last_error FROM jobs WHERE type='transcribe_media'`).
		Scan(&state, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != "dead" || attempts != 1 || !strings.Contains(lastErr, "thời lượng") {
		t.Fatalf("job state=%q attempts=%d err=%q", state, attempts, lastErr)
	}
	row, err := st.MediaByID(ctx, res.MediaID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "dead" {
		t.Fatalf("media status=%q", row.Status)
	}
	if fake.OmniCalls != 0 {
		t.Fatalf("đã gọi omni: %d", fake.OmniCalls)
	}
}

// TestTranscribeChainRecallFindsTranscript (M6 verify): chuỗi trọn vẹn
// audio → worker (transcribe + embed) → recall tìm thấy transcript. Không mạng.
func TestTranscribeChainRecallFindsTranscript(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}

	const transcript = "hôm nay họp về ngân sách quý bốn"
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string {
		return transcript
	}})
	cfg := egressfake.Config(fake, nil)
	cfg.DataDir = dir
	b := brain.New(st, fake.Egress, &cfg)
	md := media.New(st, b, fake.Egress, &cfg, nil, dir)

	spID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	res, err := md.Ingest(ctx, "../media/testdata/audio_short.m4a", spID, "cli")
	if err != nil {
		t.Fatal(err)
	}

	reg := worker.NewRegistry()
	worker.RegisterAll(reg, worker.Deps{Store: st, Brain: b, Egress: fake.Egress, Config: &cfg, DataDir: dir})
	if n, err := worker.Run(ctx, st, reg, 0, time.Now); err != nil || n != 2 {
		// transcribe_media + embed_chunk của note transcript
		t.Fatalf("run: n=%d err=%v, muốn 2", n, err)
	}
	row, err := st.MediaByID(ctx, res.MediaID)
	if err != nil || row.Status != "done" || row.Transcript == nil || *row.Transcript != transcript {
		t.Fatalf("row=%+v err=%v", row, err)
	}

	rr, err := b.Recall(ctx, brain.RecallParams{Query: "ngân sách quý bốn", SpaceIDs: []int64{spID}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range rr.Hits {
		if strings.Contains(h.Text, "ngân sách") || h.Kind == "transcript" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("recall không thấy transcript: hits=%+v stages=%v", rr.Hits, rr.Stages)
	}
}

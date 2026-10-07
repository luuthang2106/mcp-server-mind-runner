package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

// seedNoteChunks ghi note + 1 chunk thẳng qua store (không qua brain → không job).
func seedNoteChunks(t *testing.T, dataDir, text string) int64 {
	t.Helper()
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	id, _, err := st.UpsertNote(ctx, &store.Note{
		SpaceID: 1, Kind: "note", Text: text, Source: "tool:remember", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertChunks(ctx, id, []store.Chunk{{Ordinal: 0, Text: text, TokenCount: 3}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMaintenanceWarnsLowDiskAndBacksUp(t *testing.T) {
	_, dataDir := setupFresh(t)

	old := freeSpaceFn
	freeSpaceFn = func(string) (uint64, error) { return 1, nil } // 1 byte còn trống
	t.Cleanup(func() { freeSpaceFn = old })

	var out, errb bytes.Buffer
	code := RunMaintenance(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "warn: đĩa còn") {
		t.Fatalf("thiếu warn đĩa\nout=%s", s)
	}
	if !strings.Contains(s, "checkpoint: ok") || !strings.Contains(s, "quick_check: ok") {
		t.Fatalf("out=%s", s)
	}
	matches, _ := filepath.Glob(filepath.Join(dataDir, "backups", "mind-runner-*.db"))
	if len(matches) != 1 {
		t.Fatalf("backup: %v", matches)
	}
}

func TestMaintenanceMergesSpool(t *testing.T) {
	_, dataDir := setupFresh(t)

	// seed 1 file spool (cần session row trước vì FK)
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	tp := "/tmp/t.jsonl"
	if err := st.UpsertSessionStart(context.Background(), "sess-spool", "claude-code", 1, &tp, time.Now()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	spoolDir := filepath.Join(dataDir, "spool")
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spoolDir, "sess-spool-123.gz"), []byte("blob"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := RunMaintenance(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "spool: merged=1") {
		t.Fatalf("out=%s", out.String())
	}
	entries, err := os.ReadDir(spoolDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool còn %d file", len(entries))
	}

	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM session_raw`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("session_raw=%d", n)
	}
}

// TestMaintenanceProcessesQueuedEmbedJob: seed 1 job embed_chunk → maintenance
// chạy queue → job done + embedding đủ (egress giả qua env base URL).
func TestMaintenanceProcessesQueuedEmbedJob(t *testing.T) {
	_, dataDir := setupFresh(t)
	fake := egressfake.New(t, egressfake.Options{})
	t.Setenv("MIND_RUNNER_GATEWAY_BASE_URL", fake.URL)

	noteID := seedNoteChunks(t, dataDir, "cần embed qua queue")
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue(context.Background(), "embed_chunk", map[string]int64{"note_id": noteID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var out, errb bytes.Buffer
	code := RunMaintenance(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	// backfill enqueue lại cùng (type,payload) → store dedupe về job seed → 1 job.
	if !strings.Contains(out.String(), "queue: processed=1") {
		t.Fatalf("out=%s", out.String())
	}

	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	counts, err := st.JobCounts(ctx)
	if err != nil || counts["done"] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
	missing, err := st.ChunksMissingEmbeddings(ctx, noteID, fake.Egress.EmbedModel(egress.PolicyCloud))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing=%d err=%v", len(missing), err)
	}
}

// TestMaintenancePurgesExpiredEvents: note sự kiện quá events_days → purge
// trong maintenance, in dòng "purge: …" (xoá không im lặng) và row biến mất.
func TestMaintenancePurgesExpiredEvents(t *testing.T) {
	_, dataDir := setupFresh(t)

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -400)
	id, _, err := st.UpsertNote(context.Background(), &store.Note{
		SpaceID: 1, Kind: "note", Text: "sự kiện quá hạn", Source: "tool:remember", CreatedAt: old, UpdatedAt: old,
	})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	var out, errb bytes.Buffer
	code := RunMaintenance(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "purge: events=1") {
		t.Fatalf("out=%s", out.String())
	}

	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM notes WHERE id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("note quá hạn chưa bị purge")
	}
}

// TestMaintenanceRetryDead: dead job → --retry-dead đưa về queued (attempts=0)
// và queue chạy nó trong cùng lần maintenance.
func TestMaintenanceRetryDead(t *testing.T) {
	_, dataDir := setupFresh(t)
	fake := egressfake.New(t, egressfake.Options{})
	t.Setenv("MIND_RUNNER_GATEWAY_BASE_URL", fake.URL)

	noteID := seedNoteChunks(t, dataDir, "note cho dead job")
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	jobID, err := st.Enqueue(ctx, "embed_chunk", map[string]int64{"note_id": noteID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE jobs SET state='dead', attempts=5, last_error='x' WHERE id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var out, errb bytes.Buffer
	code := RunMaintenance([]string{"--retry-dead"}, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "retry-dead: 1") {
		t.Fatalf("out=%s", out.String())
	}

	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	counts, err := st.JobCounts(ctx)
	if err != nil || counts["dead"] != 0 || counts["done"] != 2 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
	missing, err := st.ChunksMissingEmbeddings(ctx, noteID, fake.Egress.EmbedModel(egress.PolicyCloud))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing=%d err=%v", len(missing), err)
	}
}

// TestMaintenanceBackfillsMissingEmbeddings: note+chunk KHÔNG job → maintenance
// tự enqueue backfill rồi xử lý → embedding đủ.
func TestMaintenanceBackfillsMissingEmbeddings(t *testing.T) {
	_, dataDir := setupFresh(t)
	fake := egressfake.New(t, egressfake.Options{})
	t.Setenv("MIND_RUNNER_GATEWAY_BASE_URL", fake.URL)

	noteID := seedNoteChunks(t, dataDir, "note chưa từng có job embed")

	var out, errb bytes.Buffer
	code := RunMaintenance(nil, &out, &errb, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "backfill: 1 note") {
		t.Fatalf("out=%s", out.String())
	}

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	missing, err := st.ChunksMissingEmbeddings(ctx, noteID, fake.Egress.EmbedModel(egress.PolicyCloud))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing=%d err=%v", len(missing), err)
	}
	counts, err := st.JobCounts(ctx)
	if err != nil || counts["done"] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
}

// TestMaintenanceScansWatchDirs: [media].watch_dirs có file audio mtime cũ →
// maintenance copy vào media/ + enqueue transcribe_media (queue đã chạy trước
// watch nên job nằm chờ, không cần mạng) + in đủ số lượng.
func TestMaintenanceScansWatchDirs(t *testing.T) {
	cfgPath, dataDir := setupFresh(t)

	watch := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	src := filepath.Join(watch, "ghi-am.m4a")
	if err := os.WriteFile(src, []byte("audio cũ"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Media.WatchDirs = []string{watch}
	if err := writeConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := RunMaintenance(nil, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "watch: seen=1 ingested=1 fresh=0 type=0 already=0") {
		t.Fatalf("out=%s", out.String())
	}

	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var rel, status string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT path, status FROM media WHERE source=?`, "watch:"+watch).Scan(&rel, &status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("status=%q", status)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "media", rel)); err != nil {
		t.Fatalf("file chưa copy: %v", err)
	}
	var jobs int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE type='transcribe_media' AND state='queued'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("jobs=%d, muốn 1", jobs)
	}
}

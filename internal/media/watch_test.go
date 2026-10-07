package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress/egressfake"
)

// TestScanWatchDirs: chỉ quét file trực tiếp (không đệ quy), bỏ mtime < 60s và
// đuôi không hỗ trợ; lần 2 → AlreadyHave, không job mới; space theo [spaces.match].
func TestScanWatchDirs(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{})
	b := brain.New(st, fake.Egress, cfg)
	fr := &fakeRunner{}
	md := New(st, b, fake.Egress, cfg, fr, dir)

	watch := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	stale := filepath.Join(watch, "cũ.m4a")
	writeFile(t, stale, []byte("audio cũ"))
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(watch, "đang-ghi.m4a"), []byte("mtime now")) // fresh → skip
	la := filepath.Join(watch, "lạ.xyz")
	writeFile(t, la, []byte("x"))
	if err := os.Chtimes(la, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(watch, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(watch, "sub", "ẩn.m4a"), []byte("không đệ quy"))

	// dir watch nằm trong glob → space work
	cfg.Spaces.Match.Rules = []config.MatchRule{{Glob: filepath.Join(watch, "*"), Space: "work"}}

	rep, err := md.ScanWatchDirs(ctx, []string{watch}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (WatchReport{Seen: 3, Ingested: 1, SkippedFresh: 1, SkippedType: 1}) {
		t.Fatalf("rep=%+v", rep)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("audio nguyên bản phải không shell-out: %v", fr.calls)
	}

	// row vào space work + source watch:<dir> + sha khớp file nguồn
	workID, err := st.SpaceByName(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	wantSHA, err := SHA256File(stale)
	if err != nil {
		t.Fatal(err)
	}
	var spaceID int64
	var sha, source string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT space_id, sha256, source FROM media`).Scan(&spaceID, &sha, &source); err != nil {
		t.Fatal(err)
	}
	if spaceID != workID || sha != wantSHA || source != "watch:"+watch {
		t.Fatalf("space=%d sha=%s source=%q", spaceID, sha, source)
	}

	// đúng 1 job transcribe_media
	if jobs := transcribeJobs(t, st); len(jobs) != 1 {
		t.Fatalf("jobs=%v", jobs)
	}

	// quét lần 2 → AlreadyHave, không job mới, không row mới
	rep2, err := md.ScanWatchDirs(ctx, []string{watch}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep2 != (WatchReport{Seen: 3, SkippedFresh: 1, SkippedType: 1, AlreadyHave: 1}) {
		t.Fatalf("rep2=%+v", rep2)
	}
	if jobs := transcribeJobs(t, st); len(jobs) != 1 {
		t.Fatalf("job mới ở lần 2: %v", jobs)
	}
	var rows int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM media`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows=%d, muốn 1 (không đệ quy + dedupe)", rows)
	}
}

// TestScanWatchDirsContinuesOnIngestError: file lỗi (avconvert hỏng) đứng trước
// theo thứ tự tên → đếm Failed, vẫn ingest file sau.
func TestScanWatchDirsContinuesOnIngestError(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{})
	b := brain.New(st, fake.Egress, cfg)
	fr := &fakeRunner{fn: func(string, []string) ([]byte, error) { return nil, errors.New("avconvert hỏng") }}
	md := New(st, b, fake.Egress, cfg, fr, dir)

	watch := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range []string{"a-hỏng.mov", "b-tốt.m4a"} {
		p := filepath.Join(watch, name)
		writeFile(t, p, []byte(name))
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := md.ScanWatchDirs(ctx, []string{watch}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep != (WatchReport{Seen: 2, Ingested: 1, Failed: 1}) {
		t.Fatalf("rep=%+v", rep)
	}
}

package media

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/config"
	"mind-runner/internal/store"
)

// setupMedia: store đã migrate (spaces seed sẵn) + config DataDir trỏ temp.
func setupMedia(t *testing.T) (*store.Store, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = dir
	return st, &cfg, dir
}

// transcribeJobs trả payload các job transcribe_media.
func transcribeJobs(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT payload FROM jobs WHERE type='transcribe_media' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// TestIngestAudioDedupe: file audio → row queued + file copy trong media/ + 1 job;
// lặp lại → Created=false, cùng id, không job thứ 2.
func TestIngestAudioDedupe(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	spID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	md := New(st, nil, nil, cfg, &fakeRunner{}, dir)

	const src = "testdata/audio_short.m4a"
	res, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !res.Created || res.Status != "queued" || res.MediaID <= 0 {
		t.Fatalf("res=%+v", res)
	}

	row, err := st.MediaByID(ctx, res.MediaID)
	if err != nil || row == nil {
		t.Fatalf("row: %+v err=%v", row, err)
	}
	sha, err := SHA256File(src)
	if err != nil {
		t.Fatal(err)
	}
	if row.Kind != "audio" || row.SHA256 != sha || row.Source != "cli" || row.Status != "queued" {
		t.Fatalf("row=%+v", row)
	}

	// file copy byte-exact tại media/<rel>
	stored, err := os.ReadFile(filepath.Join(dir, "media", row.Path))
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, orig) || row.Bytes != int64(len(orig)) {
		t.Fatalf("file copy lệch: bytes=%d len=%d", row.Bytes, len(orig))
	}

	// đúng 1 job, payload trỏ media_id
	jobs := transcribeJobs(t, st)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%v", jobs)
	}
	var pl struct {
		MediaID int64 `json:"media_id"`
	}
	if err := json.Unmarshal([]byte(jobs[0]), &pl); err != nil || pl.MediaID != res.MediaID {
		t.Fatalf("payload=%s err=%v", jobs[0], err)
	}

	// lặp → "đã có", không job mới
	res2, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatalf("Ingest lần 2: %v", err)
	}
	if res2.Created || res2.MediaID != res.MediaID || res2.Status != "queued" {
		t.Fatalf("lần 2: %+v", res2)
	}
	if jobs = transcribeJobs(t, st); len(jobs) != 1 {
		t.Fatalf("job thứ 2 xuất hiện: %v", jobs)
	}
}

// TestIngestImageCopiesOriginal: ảnh copy NGUYÊN BẢN byte-exact (không convert — D9).
func TestIngestImageCopiesOriginal(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	spID, _ := st.SpaceByName(ctx, "personal")
	md := New(st, nil, nil, cfg, &fakeRunner{}, dir)

	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "ảnh.jpg")
	writeFile(t, src, buf.Bytes())

	res, err := md.Ingest(ctx, src, spID, "tool:ingest")
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.MediaByID(ctx, res.MediaID)
	if err != nil || row.Kind != "image" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "media", row.Path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, buf.Bytes()) {
		t.Fatal("ảnh lưu không byte-exact")
	}
}

// TestIngestVideoExtractsAudio: video → avconvert (fake) → lưu file m4a;
// sha256 dedupe theo file NGUỒN (.mov), không copy container.
func TestIngestVideoExtractsAudio(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	spID, _ := st.SpaceByName(ctx, "personal")
	fr := &fakeRunner{fn: func(name string, args []string) ([]byte, error) {
		if name != "avconvert" {
			t.Fatalf("lệnh lạ: %s", name)
		}
		return nil, os.WriteFile(args[len(args)-1], []byte("m4a-giả"), 0o600)
	}}
	md := New(st, nil, nil, cfg, fr, dir)

	src := filepath.Join(t.TempDir(), "video.mov")
	writeFile(t, src, []byte("container-mov-giả"))
	res, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.MediaByID(ctx, res.MediaID)
	if err != nil || row == nil {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	sha, _ := SHA256File(src) // sha file nguồn, không phải m4a
	if row.Kind != "video" || row.SHA256 != sha || !strings.HasSuffix(row.Path, ".m4a") {
		t.Fatalf("row=%+v", row)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "media", row.Path))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "m4a-giả" {
		t.Fatalf("file lưu = %q, muốn audio đã trích", stored)
	}
}

// TestIngestUnsupportedVideo: container khác → lỗi kèm hướng dẫn mp4/mov.
func TestIngestUnsupportedVideo(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	spID, _ := st.SpaceByName(ctx, "personal")
	md := New(st, nil, nil, cfg, &fakeRunner{}, dir)

	src := filepath.Join(t.TempDir(), "phim.mkv")
	writeFile(t, src, []byte("x"))
	_, err := md.Ingest(ctx, src, spID, "cli")
	if err == nil || !strings.Contains(err.Error(), "mp4") {
		t.Fatalf("err=%v, muốn hướng dẫn mp4/mov", err)
	}
}

// TestIsMedia: nhận đuôi media (kể cả chưa hỗ trợ), không nhận file text.
func TestIsMedia(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"a.m4a", true}, {"a.MOV", true}, {"ảnh.HEIC", true}, {"phim.mkv", true}, {"x.gif", true},
		{"note.md", false}, {"t.txt", false}, {"không-đuôi", false},
	} {
		if got := IsMedia(tc.path); got != tc.want {
			t.Errorf("IsMedia(%q)=%v, muốn %v", tc.path, got, tc.want)
		}
	}
}

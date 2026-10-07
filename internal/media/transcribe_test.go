package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/execx"
)

// ladderFake: fake afconvert luôn tạo file 8MB (base64 ≈ 10.7MB > trần 10MiB)
// → cạn ladder bất kể mức nào.
func ladderFake(t *testing.T) *fakeRunner {
	t.Helper()
	big := bytes.Repeat([]byte{7}, 8_000_000)
	return &fakeRunner{fn: func(_ string, args []string) ([]byte, error) {
		return nil, os.WriteFile(args[len(args)-1], big, 0o600)
	}}
}

// TestTranscribeAudio: audio ≤ trần → omni (m4a nguyên bản, không shell-out) →
// media done + note kind transcript source media:<id> + embed job.
func TestTranscribeAudio(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string {
		return "xin chào mind runner"
	}})
	fr := &fakeRunner{}
	b := brain.New(st, fake.Egress, cfg)
	md := New(st, b, fake.Egress, cfg, fr, dir)
	spID, _ := st.SpaceByName(ctx, "personal")

	res, err := md.Ingest(ctx, "testdata/audio_short.m4a", spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := md.Transcribe(ctx, res.MediaID); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	row, err := st.MediaByID(ctx, res.MediaID)
	if err != nil || row.Status != "done" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if row.Transcript == nil || *row.Transcript != "xin chào mind runner" ||
		row.Model == nil || *row.Model != "fake-omni" {
		t.Fatalf("row=%+v", row)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("audio ≤ trần phải gửi nguyên, đã shell-out: %v", fr.calls)
	}

	// note transcript + chunks + embed job
	var noteID int64
	var kind, text string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT id, kind, text FROM notes WHERE source=?`, "media:"+strconv.FormatInt(res.MediaID, 10)).Scan(&noteID, &kind, &text); err != nil {
		t.Fatal(err)
	}
	if kind != "transcript" || text != "xin chào mind runner" {
		t.Fatalf("note kind=%q text=%q", kind, text)
	}
	var nChunks int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE note_id=?`, noteID).Scan(&nChunks); err != nil || nChunks == 0 {
		t.Fatalf("chunks=%d err=%v", nChunks, err)
	}
	counts, err := st.JobCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["queued"] != 2 || counts["done"] != 0 {
		// transcribe_media (gọi trực tiếp, không qua worker) + embed_chunk của note
		t.Fatalf("counts=%v", counts)
	}

	// request omni: audio m4a + prompt text
	if len(fake.OmniParts) != 2 || fake.OmniParts[0]["type"] != "input_audio" {
		t.Fatalf("parts=%v", fake.OmniParts)
	}
}

// TestTranscribeImage: ảnh → caption (part image_url, không audio) → note kind caption.
func TestTranscribeImage(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string {
		return "ảnh nền xanh có hoạ tiết"
	}})
	b := brain.New(st, fake.Egress, cfg)
	md := New(st, b, fake.Egress, cfg, &fakeRunner{}, dir)
	spID, _ := st.SpaceByName(ctx, "personal")

	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	img.Set(1, 1, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "ảnh.jpg")
	writeFile(t, src, buf.Bytes())

	res, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := md.Transcribe(ctx, res.MediaID); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	row, _ := st.MediaByID(ctx, res.MediaID)
	if row.Status != "done" || row.Transcript == nil || *row.Transcript != "ảnh nền xanh có hoạ tiết" {
		t.Fatalf("row=%+v", row)
	}
	var kind string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT kind FROM notes WHERE source=?`, "media:"+strconv.FormatInt(res.MediaID, 10)).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "caption" {
		t.Fatalf("kind=%q", kind)
	}
	if len(fake.OmniParts) != 2 || fake.OmniParts[0]["type"] != "image_url" {
		t.Fatalf("parts=%v", fake.OmniParts)
	}
}

// TestTranscribeVideoRealExtract: chỉ darwin — video .mov thật → avconvert thật
// trích audio → omni endpoint fake: pipeline video trọn vẹn, không mạng.
func TestTranscribeVideoRealExtract(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("cần macOS (avconvert)")
	}
	const src = "testdata/video_short.mov"
	if _, err := os.Stat(src); err != nil {
		t.Skipf("thiếu fixture: %v", err)
	}
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string {
		return "video ghi âm thử"
	}})
	b := brain.New(st, fake.Egress, cfg)
	md := New(st, b, fake.Egress, cfg, execx.OS{}, dir)
	spID, _ := st.SpaceByName(ctx, "personal")

	res, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.MediaByID(ctx, res.MediaID)
	if err != nil || row.Kind != "video" || !strings.HasSuffix(row.Path, ".m4a") || row.Bytes == 0 {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if err := md.Transcribe(ctx, res.MediaID); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	row, _ = st.MediaByID(ctx, res.MediaID)
	if row.Status != "done" || row.Transcript == nil || *row.Transcript != "video ghi âm thử" {
		t.Fatalf("row=%+v", row)
	}
	if len(fake.OmniParts) != 2 || fake.OmniParts[0]["type"] != "input_audio" {
		t.Fatalf("parts=%v", fake.OmniParts)
	}
	ia, _ := fake.OmniParts[0]["input_audio"].(map[string]any)
	if ia["format"] != "m4a" {
		t.Fatalf("format=%v", ia["format"])
	}
}

// TestTranscribeMissingFileFails: file biến mất giữa chừng → fail kèm lý do cụ thể.
func TestTranscribeMissingFileFails(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{})
	b := brain.New(st, fake.Egress, cfg)
	md := New(st, b, fake.Egress, cfg, &fakeRunner{}, dir)
	spID, _ := st.SpaceByName(ctx, "personal")

	res, err := md.Ingest(ctx, "testdata/audio_short.m4a", spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := st.MediaByID(ctx, res.MediaID)
	if err := os.Remove(filepath.Join(dir, "media", row.Path)); err != nil {
		t.Fatal(err)
	}
	err = md.Transcribe(ctx, res.MediaID)
	if err == nil || !strings.Contains(err.Error(), row.Path) {
		t.Fatalf("err=%v, muốn kèm đường dẫn %s", err, row.Path)
	}
	row, _ = st.MediaByID(ctx, res.MediaID)
	if row.Status != "failed" || row.LastError == "" {
		t.Fatalf("row=%+v", row)
	}
	if fake.OmniCalls != 0 {
		t.Fatalf("đã gọi omni: %d", fake.OmniCalls)
	}
}

// TestTranscribeOverCapDead: audio vượt trần, ladder cạn → ErrTooLarge+ErrPermanent,
// media status dead kèm "thời lượng".
func TestTranscribeOverCapDead(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{})
	b := brain.New(st, fake.Egress, cfg)
	fr := ladderFake(t)
	md := New(st, b, fake.Egress, cfg, fr, dir)
	spID, _ := st.SpaceByName(ctx, "personal")

	src := filepath.Join(t.TempDir(), "dài.m4a")
	writeFile(t, src, bytes.Repeat([]byte{7}, 7_900_000)) // base64 > 10MB
	res, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	err = md.Transcribe(ctx, res.MediaID)
	if !errors.Is(err, ErrPermanent) || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err=%v, muốn cả ErrPermanent lẫn ErrTooLarge", err)
	}
	if len(fr.calls) != 3 {
		t.Fatalf("ladder chạy %d lần, muốn 3", len(fr.calls))
	}
	row, _ := st.MediaByID(ctx, res.MediaID)
	if row.Status != "dead" || !strings.Contains(row.LastError, "thời lượng") {
		t.Fatalf("row=%+v", row)
	}
	if fake.OmniCalls != 0 {
		t.Fatalf("đã gọi omni: %d", fake.OmniCalls)
	}
}

// TestTranscribeDeletesOriginalsWhenConfigured (6.5): keep_originals=false →
// transcribe xong xoá file gốc, path=”, row + transcript + note còn nguyên;
// keep_originals=true → file giữ, path nguyên.
func TestTranscribeDeletesOriginalsWhenConfigured(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string {
		return "nội dung ghi âm"
	}})
	b := brain.New(st, fake.Egress, cfg)
	md := New(st, b, fake.Egress, cfg, &fakeRunner{}, dir)
	spID, _ := st.SpaceByName(ctx, "personal")

	cfg.Media.KeepOriginals = false
	res, err := md.Ingest(ctx, "testdata/audio_short.m4a", spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := st.MediaByID(ctx, res.MediaID)
	rel := row.Path
	if err := md.Transcribe(ctx, res.MediaID); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	row, _ = st.MediaByID(ctx, res.MediaID)
	if row.Status != "done" || row.Path != "" {
		t.Fatalf("row=%+v, muốn done + path rỗng", row)
	}
	if row.Transcript == nil || *row.Transcript != "nội dung ghi âm" {
		t.Fatalf("transcript=%v", row.Transcript)
	}
	if _, err := os.Stat(filepath.Join(dir, "media", rel)); !os.IsNotExist(err) {
		t.Fatalf("file gốc chưa bị xoá: %v", err)
	}
	var nNotes int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notes WHERE source=?`, "media:"+strconv.FormatInt(res.MediaID, 10)).Scan(&nNotes); err != nil || nNotes != 1 {
		t.Fatalf("notes=%d err=%v, muốn note giữ lại", nNotes, err)
	}

	// keep_originals=true → file giữ nguyên
	cfg.Media.KeepOriginals = true
	data, err := os.ReadFile("testdata/audio_short.m4a")
	if err != nil {
		t.Fatal(err)
	}
	src2 := filepath.Join(t.TempDir(), "giữ.m4a")
	writeFile(t, src2, append(data, 0x00)) // nội dung khác → sha khác → row mới
	res2, err := md.Ingest(ctx, src2, spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := md.Transcribe(ctx, res2.MediaID); err != nil {
		t.Fatalf("Transcribe 2: %v", err)
	}
	row2, _ := st.MediaByID(ctx, res2.MediaID)
	if row2.Status != "done" || row2.Path == "" {
		t.Fatalf("row2=%+v, muốn done + path nguyên", row2)
	}
	if _, err := os.Stat(filepath.Join(dir, "media", row2.Path)); err != nil {
		t.Fatalf("file phải còn: %v", err)
	}
}

// TestTranscribeShrunkAudioFormat: wav vượt trần → ShrinkAudio ra .m4a → format
// gửi omni phải là m4a (theo file thực gửi), không phải wav.
func TestTranscribeShrunkAudioFormat(t *testing.T) {
	st, cfg, dir := setupMedia(t)
	ctx := context.Background()
	fake := egressfake.New(t, egressfake.Options{OmniResp: func([]map[string]any) string { return "ok" }})
	fr := &fakeRunner{fn: func(_ string, args []string) ([]byte, error) {
		return nil, os.WriteFile(args[len(args)-1], []byte("m4a nhỏ"), 0o600)
	}}
	b := brain.New(st, fake.Egress, cfg)
	md := New(st, b, fake.Egress, cfg, fr, dir)
	spID, _ := st.SpaceByName(ctx, "personal")

	src := filepath.Join(t.TempDir(), "dài.wav")
	writeFile(t, src, bytes.Repeat([]byte{1}, 8_000_000))
	res, err := md.Ingest(ctx, src, spID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := md.Transcribe(ctx, res.MediaID); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	ia, _ := fake.OmniParts[0]["input_audio"].(map[string]any)
	if ia["format"] != "m4a" || !strings.HasPrefix(ia["data"].(string), "data:audio/m4a;") {
		t.Fatalf("input_audio format=%v", ia["format"])
	}
}

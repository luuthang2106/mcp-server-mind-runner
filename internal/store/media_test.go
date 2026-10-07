package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }
func strp(v string) *string  { return &v }

// TestMediaInsertDedupeAndLifecycle: insert mới/dedupe theo (space, sha)/khác
// space; update status kèm-và-không-kèm lý do; set transcript + model.
func TestMediaInsertDedupeAndLifecycle(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	spPersonal, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	spWork, err := st.SpaceByName(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}

	m := &Media{SpaceID: spPersonal, SHA256: "ab12cd", Kind: "audio", Path: "ab/ab12cd.m4a",
		Bytes: 1000, Status: "queued", Source: "cli", CreatedAt: now, UpdatedAt: now}
	id1, created, err := st.InsertMedia(ctx, m, now)
	if err != nil || !created || id1 <= 0 {
		t.Fatalf("insert: id=%d created=%v err=%v", id1, created, err)
	}

	// trùng (space, sha) → created=false, không ghi đè row cũ
	id2, created2, err := st.InsertMedia(ctx, &Media{SpaceID: spPersonal, SHA256: "ab12cd", Kind: "video",
		Path: "zz/đè.m4a", Bytes: 999, Status: "queued", Source: "watch:x", CreatedAt: now, UpdatedAt: now}, now)
	if err != nil || created2 || id2 != 0 {
		t.Fatalf("trùng: id=%d created=%v err=%v", id2, created2, err)
	}
	got, err := st.MediaBySHA(ctx, spPersonal, "ab12cd")
	if err != nil || got == nil {
		t.Fatalf("MediaBySHA: %v %v", got, err)
	}
	if got.Path != "ab/ab12cd.m4a" || got.Bytes != 1000 || got.Kind != "audio" || got.Source != "cli" {
		t.Fatalf("row bị đổi: %+v", got)
	}
	if !got.CreatedAt.Equal(now) || got.Status != "queued" || got.DurationS != nil || got.Transcript != nil {
		t.Fatalf("giá trị mặc định sai: %+v", got)
	}

	// khác space → row mới, duration roundtrip qua cột REAL
	id3, created3, err := st.InsertMedia(ctx, &Media{SpaceID: spWork, SHA256: "ab12cd", Kind: "audio",
		Path: "ab/ab12cd.m4a", Bytes: 1000, DurationS: f64(12.5), Status: "queued", Source: "cli",
		CreatedAt: now, UpdatedAt: now}, now)
	if err != nil || !created3 || id3 == id1 {
		t.Fatalf("space khác: id=%d created=%v err=%v", id3, created3, err)
	}
	byID, err := st.MediaByID(ctx, id3)
	if err != nil || byID == nil || byID.DurationS == nil || *byID.DurationS != 12.5 {
		t.Fatalf("MediaByID: %+v err=%v", byID, err)
	}

	// status processing, không kèm lý do → last_error giữ nguyên
	if err := st.UpdateMediaStatus(ctx, id1, "processing", nil, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = st.MediaByID(ctx, id1)
	if err != nil || got.Status != "processing" || got.LastError != "" || !got.UpdatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("processing: %+v err=%v", got, err)
	}

	// failed kèm lý do → last_error ghi lại
	if err := st.UpdateMediaStatus(ctx, id1, "failed", strp("omni 500"), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = st.MediaByID(ctx, id1)
	if got.Status != "failed" || got.LastError != "omni 500" {
		t.Fatalf("failed: %+v", got)
	}

	// transcript + model; status không bị đụng
	if err := st.SetMediaTranscript(ctx, id1, "xin chào mind runner", "qwen3.8-omni-flash", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = st.MediaByID(ctx, id1)
	if got.Transcript == nil || *got.Transcript != "xin chào mind runner" ||
		got.Model == nil || *got.Model != "qwen3.8-omni-flash" || got.Status != "failed" {
		t.Fatalf("transcript: %+v", got)
	}

	// sha không tồn tại / id không tồn tại → (nil, nil)
	if m0, err := st.MediaBySHA(ctx, spPersonal, "không-có"); err != nil || m0 != nil {
		t.Fatalf("sha lạ: %+v err=%v", m0, err)
	}
	if m0, err := st.MediaByID(ctx, 9999); err != nil || m0 != nil {
		t.Fatalf("id lạ: %+v err=%v", m0, err)
	}
}

// TestClearMediaPathAndMissingPaths (6.5): MediaMissingPaths chỉ báo path
// non-empty mà file không còn; path=” (đã xoá chủ đích) không báo; sau
// ClearMediaPath row vẫn còn nhưng path rỗng → hết báo thiếu.
func TestClearMediaPathAndMissingPaths(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	sp, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "aa"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aa", "có.m4a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aa", "xoá.m4a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	insert := func(sha, path string) int64 {
		t.Helper()
		id, created, err := st.InsertMedia(ctx, &Media{SpaceID: sp, SHA256: sha, Kind: "audio",
			Path: path, Bytes: 1, Status: "done", Source: "cli", CreatedAt: now, UpdatedAt: now}, now)
		if err != nil || !created {
			t.Fatalf("insert %s: id=%d created=%v err=%v", sha, id, created, err)
		}
		return id
	}
	insert("h1", "aa/có.m4a")
	insert("h2", "aa/thiếu.m4a")
	idCleared := insert("h3", "aa/xoá.m4a")

	missing, err := st.MediaMissingPaths(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "aa/thiếu.m4a" {
		t.Fatalf("missing=%v, muốn [aa/thiếu.m4a]", missing)
	}

	// dropOriginal: xoá file rồi ClearMediaPath → row không còn bị báo thiếu
	if err := os.Remove(filepath.Join(dir, "aa", "xoá.m4a")); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearMediaPath(ctx, idCleared, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.MediaByID(ctx, idCleared)
	if err != nil || got == nil || got.Path != "" {
		t.Fatalf("clear: %+v err=%v", got, err)
	}
	if missing, err = st.MediaMissingPaths(ctx, dir); err != nil || len(missing) != 1 || missing[0] != "aa/thiếu.m4a" {
		t.Fatalf("sau clear: missing=%v err=%v, muốn đúng 1 (thiếu.m4a)", missing, err)
	}
}

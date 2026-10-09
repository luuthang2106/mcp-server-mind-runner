package brain

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"mind-runner/internal/config"
	"mind-runner/internal/store"
)

// testCfg config tối thiểu cho Brain khi test không cần policy riêng:
// mọi space theo mặc định "cloud".
func testCfg() *config.Config {
	cfg := config.Default()
	return &cfg
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background(), filepath.Join(t.TempDir(), "backups")); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestWriteNoteCreatesNoteChunksJob(t *testing.T) {
	st := newStore(t)
	b := New(st, nil, testCfg())
	ctx := context.Background()
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}

	res, err := b.WriteNote(ctx, WriteParams{
		SpaceID: spaceID, Kind: "note", Text: "câu một. câu hai.", Tags: []string{"x"}, Source: "tool:remember",
	})
	if err != nil || !res.Fresh {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	n, err := st.FetchNote(ctx, res.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != "note" || n.SpaceID != spaceID || n.Source != "tool:remember" {
		t.Fatalf("note=%+v", n)
	}
	chunks, err := st.ChunksOfNote(ctx, res.NoteID)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("chunks=%+v err=%v", chunks, err)
	}

	var jobType, payload, state string
	if err := st.DB().QueryRowContext(ctx, `SELECT type, payload, state FROM jobs`).Scan(&jobType, &payload, &state); err != nil {
		t.Fatal(err)
	}
	var p struct {
		NoteID int64 `json:"note_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil || p.NoteID != res.NoteID {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
	if jobType != "embed_chunk" || state != "queued" {
		t.Fatalf("type=%s state=%s", jobType, state)
	}

	// gọi lần 2 → "ôn lại": không chunk mới, không job mới
	res2, err := b.WriteNote(ctx, WriteParams{
		SpaceID: spaceID, Kind: "note", Text: "câu một. câu hai.", Tags: []string{"x"}, Source: "tool:remember",
	})
	if err != nil || res2.Fresh || res2.NoteID != res.NoteID {
		t.Fatalf("res2=%+v err=%v", res2, err)
	}
	if chunks, _ := st.ChunksOfNote(ctx, res.NoteID); len(chunks) != 1 {
		t.Fatalf("chunks sau ôn lại=%d", len(chunks))
	}
	counts, err := st.JobCounts(ctx)
	if err != nil || counts["queued"] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
}

func TestWriteNoteValidation(t *testing.T) {
	st := newStore(t)
	b := New(st, nil, testCfg())
	ctx := context.Background()
	spaceID, _ := st.SpaceByName(ctx, "personal")

	if _, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "sai", Text: "x", Source: "tool:remember"}); err == nil {
		t.Fatal("kind sai phải lỗi")
	}
	for _, text := range []string{"", "   \n  "} {
		if _, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: text, Source: "tool:remember"}); err == nil {
			t.Fatalf("text %q phải lỗi", text)
		}
	}
}

// TestWriteNoteChunksRedactPrivateKey: PEM dài bị chunker cắt nhiều mảnh —
// không chunk nào (thứ được embed/rerank gửi đi) còn chứa base64 của key.
func TestWriteNoteChunksRedactPrivateKey(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	b := New(st, nil, testCfg())
	spaceID, _ := st.SpaceByName(ctx, "personal")

	body := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 80) // ~2800 ký tự, không xuống dòng
	text := "ghi chú server.\n\n-----BEGIN OPENSSH PRIVATE KEY-----\n" + body + "\n-----END OPENSSH PRIVATE KEY-----\n\nhết."
	res, err := b.WriteNote(ctx, WriteParams{SpaceID: spaceID, Kind: "note", Text: text, Source: "tool:remember"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT text FROM chunks WHERE note_id=?`, res.NoteID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		n++
		if strings.Contains(c, "QUJDREVGR0hJSktM") {
			t.Fatalf("chunk còn key material: %.80q", c)
		}
	}
	if n == 0 {
		t.Fatal("không có chunk")
	}
}

// TestWriteNoteProcedureKind: procedure là kind hợp lệ (tầng kiến thức).
func TestWriteNoteProcedureKind(t *testing.T) {
	st := newStore(t)
	b := New(st, nil, testCfg())
	ctx := context.Background()
	spaceID, err := st.SpaceByName(ctx, "personal")
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.WriteNote(ctx, WriteParams{
		SpaceID: spaceID, Kind: "procedure", Text: "Deploy mind-runner: make build rồi kéo .mcpb vào Claude Desktop.", Source: "tool:remember",
	})
	if err != nil || !res.Fresh {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !ValidKind("procedure") {
		t.Fatal("ValidKind(procedure) phải true")
	}
}

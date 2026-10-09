package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPurge: fixture spec §11 — (a) sự kiện 400 ngày + chuỗi chunks/embeddings/
// FTS xoá sạch; (b) kiến thức 400 ngày giữ nguyên; (c) 364 ngày giữ; (d) ghi
// lại gia hạn; (e) raw quá hạn; (f) jobs done cũ xoá / dead giữ / failed mới
// giữ; (g) task done cũ xoá / open giữ; (h) relations orphan + theo note xoá;
// (i) media row+file; (j) chạy 2 lần → lần 2 toàn 0; (k) FK ON không lỗi.
func TestPurge(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ago := func(d int) time.Time { return now.AddDate(0, 0, -d) }
	ret := Retention{EventsDays: 365, RawDays: 90, JobsDays: 30}
	mediaDir := t.TempDir()

	// --- (a) event note 400 ngày + chunk + embedding + FTS
	evID, _, err := s.UpsertNote(ctx, noteAt(1, "note", "sự kiện cũ 400 ngày", ago(400)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertChunks(ctx, evID, []Chunk{{Ordinal: 0, Text: "sự kiện cũ 400 ngày", TokenCount: 3}}); err != nil {
		t.Fatal(err)
	}
	var chunkID int64
	if err := s.DB().QueryRowContext(ctx, `SELECT id FROM chunks WHERE note_id=?`, evID).Scan(&chunkID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO embeddings(chunk_id, model, dim, vec, created_at) VALUES(?,?,?,?,?)`,
		chunkID, "test-model", 2, make([]byte, 8), ts(ago(400))); err != nil {
		t.Fatal(err)
	}

	// --- (a2) note đã forget quá ân hạn (deleted_at > jobs_days) → xoá cứng,
	// kể cả kiến thức (xoá thật chỉ chạy ở purge).
	fgID, _, err := s.UpsertNote(ctx, noteAt(1, "fact", "đã quên đủ lâu", ago(400)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET deleted_at=? WHERE id=?`, ts(ago(40)), fgID); err != nil {
		t.Fatal(err)
	}

	// --- (b) kiến thức 400 ngày: giữ nguyên
	knIDs := make([]int64, 0, 3)
	for _, kind := range []string{"fact", "preference", "decision"} {
		id, _, err := s.UpsertNote(ctx, noteAt(1, kind, "kiến thức vĩnh viễn "+kind, ago(400)))
		if err != nil {
			t.Fatal(err)
		}
		knIDs = append(knIDs, id)
	}

	// --- (b2) legacy superseded (cơ chế cũ đã bỏ): kiến thức, mọi tuổi —
	// purge quét sạch một lần + đếm riêng Superseded.
	spID, _, err := s.UpsertNote(ctx, noteAt(1, "decision", "quyết định bị thay thế (legacy)", ago(10)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertChunks(ctx, spID, []Chunk{{Ordinal: 0, Text: "quyết định bị thay thế (legacy)", TokenCount: 3}}); err != nil {
		t.Fatal(err)
	}
	var spChunk int64
	if err := s.DB().QueryRowContext(ctx, `SELECT id FROM chunks WHERE note_id=?`, spID).Scan(&spChunk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO embeddings(chunk_id, model, dim, vec, created_at) VALUES(?,?,?,?,?)`,
		spChunk, "test-model", 2, make([]byte, 8), ts(ago(10))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE notes SET status='superseded', superseded_by=? WHERE id=?`, knIDs[0], spID); err != nil {
		t.Fatal(err)
	}

	// --- (c) sự kiện 364 ngày: giữ (chưa quá hạn)
	cID, _, err := s.UpsertNote(ctx, noteAt(1, "note", "sự kiện 364 ngày", ago(364)))
	if err != nil {
		t.Fatal(err)
	}

	// --- (d) sự kiện 400 ngày ghi lại y hệt → updated_at=now → giữ
	dText := "sự kiện cũ được ôn lại"
	dID, _, err := s.UpsertNote(ctx, noteAt(1, "note", dText, ago(400)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertNote(ctx, noteAt(1, "note", dText, now)); err != nil {
		t.Fatal(err)
	}

	// --- session cho raw/episodes (FK)
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO sessions(id, client, space_id, started_at, last_seen_at) VALUES('sess-p','claude-code',1,?,?)`,
		ts(ago(400)), ts(now)); err != nil {
		t.Fatal(err)
	}

	// --- (e) session_raw: retained_until quá khứ → xoá; còn hạn → giữ
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO session_raw(session_id, seq, content, at, retained_until) VALUES
		 ('sess-p',1,X'00',?,?), ('sess-p',2,X'01',?,?)`,
		ts(ago(400)), ts(ago(5)), ts(ago(10)), ts(now.AddDate(0, 0, 85))); err != nil {
		t.Fatal(err)
	}

	// --- episodes: cũ xoá, mới giữ
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO episodes(session_id, seq, summary, at) VALUES
		 ('sess-p',1,'tóm tắt cũ',?), ('sess-p',2,'tóm tắt mới',?)`,
		ts(ago(400)), ts(ago(10))); err != nil {
		t.Fatal(err)
	}

	// --- (f) jobs: done cũ + dead cũ xoá, dead mới giữ, failed giữ
	jobSeed := func(state string, at time.Time) {
		t.Helper()
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO jobs(type,payload,state,attempts,run_after,created_at,updated_at) VALUES('embed_chunk','{}',?,1,?,?,?)`,
			state, ts(at), ts(at), ts(at)); err != nil {
			t.Fatal(err)
		}
	}
	jobSeed("done", ago(40))
	jobSeed("dead", ago(40))
	jobSeed("dead", ago(5))
	jobSeed("failed", ago(5))

	// --- (g) tasks: done cũ xoá, open già giữ
	tdID, _, err := s.InsertTask(ctx, 1, "việc đã xong", nil, ago(400))
	if err != nil {
		t.Fatal(err)
	}
	done := "done"
	if _, err := s.UpdateTask(ctx, tdID, &done, nil, ago(400)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InsertTask(ctx, 1, "việc còn mở", nil, ago(400)); err != nil {
		t.Fatal(err)
	}

	// --- (h) relations: orphan cũ xoá; orphan mới giữ; theo note bị xoá → xoá;
	// theo note kiến thức → giữ
	if _, err := s.InsertRelation(ctx, 1, "orphan cũ", "x", "related", nil, ago(400)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRelation(ctx, 1, "orphan mới", "x", "related", nil, ago(10)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRelation(ctx, 1, "theo note cũ", "y", "mentions", &evID, ago(10)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRelation(ctx, 1, "theo fact", "z", "related", &knIDs[0], ago(10)); err != nil {
		t.Fatal(err)
	}

	// --- (i) media: row+file cũ → xoá cả hai; row cũ thiếu file → vẫn xoá row;
	// row path='' (gốc đã xoá chủ đích, 6.5) → xoá row, không tính file
	if err := os.WriteFile(filepath.Join(mediaDir, "keep.mp3"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO media(space_id, sha256, kind, path, bytes, status, source, created_at, updated_at) VALUES
		 (1,'h1','audio','keep.mp3',5,'done','cli',?,?),
		 (1,'h2','audio','missing.flac',5,'done','cli',?,?),
		 (1,'h3','audio','',5,'done','cli',?,?)`,
		ts(ago(400)), ts(ago(400)), ts(ago(400)), ts(ago(400)), ts(ago(400)), ts(ago(400))); err != nil {
		t.Fatal(err)
	}

	// --- chạy purge
	rep, err := s.Purge(ctx, now, ret, mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	want := PurgeReport{Events: 2, Superseded: 1, Episodes: 1, Relations: 2, Tasks: 1, Raws: 1, Jobs: 2, MediaRows: 3, MediaFiles: 1}
	if rep != want {
		t.Fatalf("rep=%+v, muốn %+v", rep, want)
	}

	// chuỗi note sự kiện: notes/chunks/embeddings/chunks_fts đều sạch
	var nNotes, nChunks, nEmb, nFTS int
	for q, dst := range map[string]*int{
		`SELECT COUNT(*) FROM notes`:      &nNotes,
		`SELECT COUNT(*) FROM chunks`:     &nChunks,
		`SELECT COUNT(*) FROM embeddings`: &nEmb,
		`SELECT COUNT(*) FROM chunks_fts`: &nFTS,
	} {
		if err := s.DB().QueryRowContext(ctx, q).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if nNotes != 5 || nChunks != 0 || nEmb != 0 || nFTS != 0 {
		t.Fatalf("sau purge: notes=%d chunks=%d embeddings=%d fts=%d", nNotes, nChunks, nEmb, nFTS)
	}
	if _, err := s.FetchNote(ctx, evID); err == nil {
		t.Fatal("note sự kiện vẫn còn")
	}
	if _, err := s.FetchNote(ctx, fgID); err == nil {
		t.Fatal("note forget quá ân hạn vẫn còn")
	}
	if _, err := s.FetchNote(ctx, spID); err == nil {
		t.Fatal("note superseded legacy vẫn còn")
	}
	for _, id := range []int64{cID, dID} {
		if _, err := s.FetchNote(ctx, id); err != nil {
			t.Fatalf("note giữ sai: id=%d err=%v", id, err)
		}
	}
	// phần "giữ nguyên": dead/failed giữ, orphan mới + theo fact giữ, raw/episode/
	// task mới giữ; media không còn row nào
	kept := map[string]int{}
	for k, q := range map[string]string{
		"relations": `SELECT COUNT(*) FROM relations`,
		"dead":      `SELECT COUNT(*) FROM jobs WHERE state='dead'`,
		"failed":    `SELECT COUNT(*) FROM jobs WHERE state='failed'`,
		"raws":      `SELECT COUNT(*) FROM session_raw`,
		"episodes":  `SELECT COUNT(*) FROM episodes`,
		"tasks":     `SELECT COUNT(*) FROM tasks`,
		"media":     `SELECT COUNT(*) FROM media`,
	} {
		var n int
		if err := s.DB().QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		kept[k] = n
	}
	if kept["relations"] != 2 || kept["dead"] != 1 || kept["failed"] != 1 ||
		kept["raws"] != 1 || kept["episodes"] != 1 || kept["tasks"] != 1 || kept["media"] != 0 {
		t.Fatalf("kept=%v", kept)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, "keep.mp3")); !os.IsNotExist(err) {
		t.Fatalf("file media chưa xoá: %v", err)
	}
	// (k) FK ON → foreign_key_check sạch
	rows, err := s.DB().QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check có vi phạm")
	}

	// (j) chạy lần 2 → toàn 0
	rep2, err := s.Purge(ctx, now, ret, mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep2 != (PurgeReport{}) {
		t.Fatalf("lần 2 rep=%+v, muốn 0 hết", rep2)
	}
}

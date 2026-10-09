# kind `procedure` + job `consolidate` + bỏ hẳn supersede — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Thêm kind `procedure` (tầng kiến thức), job `consolidate` (LLM quét định kỳ gộp note trùng, xoá cứng note cũ), và bỏ hẳn cơ chế supersede (mọi thay thế note đều XOÁ CỨNG; purge quét sạch legacy).

**Architecture:** Ba thay đổi độc lập về cơ chế, ghép theo thứ tự an toàn: (1) kind mới chỉ là mở whitelist; (2) `store.HardDeleteNotes` (một tx BEGIN IMMEDIATE: relations → embeddings → chunks → notes) + purge sweep + `UpsertNote` hồi sinh row legacy — thuần additive; (3) xoá toàn bộ read/write-side của supersede, các caller chuyển sang `HardDeleteNotes`; (4) config `[consolidate]` + maintenance enqueue; (5) package `internal/consolidate` + đăng ký worker. Không migration schema — cột `status`/`superseded_by` giữ nguyên (trơ), `WHERE status='active'` giữ làm lưới an toàn cho tới khi purge quét hết legacy.

**Tech Stack:** Go (modernc SQLite, không CGO), MCP go-sdk, BurntSushi/toml; test bằng `go test` + `internal/egress/egressfake` (httptest). Go binary ở `/usr/local/go/bin/go` (không có trong PATH mặc định).

**Spec:** `docs/superpowers/specs/2026-10-09-procedure-consolidation-design.md`

## Global Constraints

- Mọi lệnh Go chạy với `PATH=/usr/local/go/bin:$PATH` (Go không nằm trong PATH mặc định).
- **KHÔNG tạo git commit / không push trong suốt plan** — user quyết định commit sau. Bỏ qua mọi bước "Commit" của skill template.
- Không migration / không sửa `internal/store/migrations/*.sql` / không drop cột `status`/`superseded_by`.
- **Không thêm dependency mới.** Không sửa các handler/tool ngoài phạm vi liệt kê.
- Comment trong code bằng tiếng Việt theo style hiện có; prompt model bằng tiếng Anh (như `prompts/extract.md`). `ponytail:` cho các heuristic có trần đã biết.
- Xoá cứng = xoá thật cả chuỗi; phục hồi chỉ từ backup ngày (giữ 7 bản). Không xoá mềm cho cơ chế thay thế.
- Sai khác nhỏ so với spec (có chủ đích): bước enqueue consolidate đặt **SAU** bước queue trong maintenance (spec ghi 2b trước queue). Lý do: tiến trình launchd chạy không key, enqueue trước queue chỉ tổ bị claim rồi hoãn 10' vô ích và làm lệch đếm `queue: processed` của mọi test hiện có; đặt sau queue → job nằm chờ sweep MCP (có key) hoặc maintenance lần sau. Task 4 cập nhật lại spec cho khớp.

---

## File Structure

| File | Hành động | Trách nhiệm |
|---|---|---|
| `internal/brain/write.go` | sửa | whitelist kind + `procedure` |
| `internal/extract/parse.go` | sửa | whitelist kind extract + `procedure` |
| `internal/extract/prompts/extract.md` | sửa | bullet `procedure` cho model |
| `internal/server/tools.go` | sửa | remember/recall kinds; supersedes → hard delete; bỏ `include_superseded` |
| `internal/server/server.go` | sửa | Instructions |
| `internal/brain/briefing.go` | sửa | bỏ marker "(đã bị #x thay)" |
| `internal/store/notes.go` | sửa | `HardDeleteNotes`, bỏ `SupersedeNote`, `UpsertNote` hồi sinh, `ActiveNotesByKinds` |
| `internal/store/purge.go` | sửa | `PurgeReport.Superseded`, sweep legacy, helper `deleteNotesChain` |
| `internal/store/search_fts.go` | sửa | `NoteFilter` luôn lọc `status='active'` |
| `internal/brain/recall.go` | sửa | bỏ `IncludeSuperseded`/`Status`/`SupersededBy` |
| `internal/extract/dedupe.go` | sửa | rename supersede→replaces; +`procedure` vào knowledgeKinds |
| `internal/extract/extract.go` | sửa | dedupe áp dụng bằng `HardDeleteNotes` |
| `internal/config/config.go` | sửa | `[consolidate].every_days` (default 7) |
| `internal/cli/maintenance.go` | sửa | bước enqueue consolidate + `consolidateDue`; in `superseded=%d` |
| `internal/consolidate/consolidate.go` | tạo | Runner job consolidate |
| `internal/consolidate/prompts/consolidate.md` | tạo | prompt gộp note |
| `internal/worker/register.go` | sửa | đăng ký `consolidate` |
| `README.md` | sửa | bảng kind, recall, `[consolidate]` |
| Tests: `harddelete_test.go` (tạo), `purge_test.go`, `structured_test.go` (store), `structured_test.go` (server), `parse_test.go`, `write_test.go`, `dedupe_test.go`, `accuracy_test.go`, `config_test.go`, `maintenance_test.go`, `consolidate_test.go` (tạo ×2) | sửa/tạo | theo từng task |

---

### Task 1: kind `procedure` (tầng kiến thức)

**Files:**
- Modify: `internal/brain/write.go` (dòng 35-40)
- Modify: `internal/extract/parse.go` (dòng 62-66)
- Modify: `internal/extract/prompts/extract.md` (mục Kinds, sau bullet `preference`)
- Modify: `internal/server/tools.go` (Kind desc dòng 31; switch dòng 66-70; recall kinds desc dòng 124; recall error dòng 178)
- Modify: `internal/server/server.go` (Instructions, sau bullet preference dòng 25)
- Modify: `README.md` (bảng kind dòng 47-53)
- Test: `internal/brain/write_test.go`, `internal/extract/parse_test.go`, `internal/server/structured_test.go`

**Interfaces:**
- Consumes: `WriteNote`/`ParseExtraction` hiện có; helpers test `newStore`/`testCfg` (`write_test.go`), `connectTest`/`call` (`structured_test.go` server).
- Produces: kind `procedure` hợp lệ qua `brain.ValidKind`, `ParseExtraction`, tool `remember`, filter `recall kinds`; note `procedure` KHÔNG nằm trong danh sách event của purge (`kind IN ('note','task_hint','transcript','caption')` — purge.go) nên vĩnh viễn, không cần code thêm.

- [ ] **Step 1: Viết test fail**

`internal/brain/write_test.go` — thêm cuối file:

```go
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
```

`internal/extract/parse_test.go` — thêm cuối file:

```go
// TestParseExtractionProcedure: procedure là kind extract hợp lệ (tầng kiến thức).
func TestParseExtractionProcedure(t *testing.T) {
	ex, err := ParseExtraction(`{"notes":[{"kind":"procedure","text":"Deploy: chạy make build rồi kéo .mcpb vào Claude Desktop."}],"tasks":[],"relations":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Notes) != 1 || ex.Notes[0].Kind != "procedure" {
		t.Fatalf("notes=%+v", ex.Notes)
	}
}
```

`internal/server/structured_test.go` — thêm test mới (giữ nguyên test cũ, sẽ sửa ở Task 3):

```go
// TestStructuredRememberProcedure: kind procedure qua tool remember + recall lọc kinds.
func TestStructuredRememberProcedure(t *testing.T) {
	cs, done := connectTest(t)
	defer done()

	var out RememberOut
	res := call(t, cs, "remember", map[string]any{"text": "Deploy mind-runner: make build rồi kéo .mcpb vào Claude Desktop", "kind": "procedure"}, &out)
	if res.IsError || out.NoteID == 0 {
		t.Fatalf("remember procedure: %+v %v", out, res.Content)
	}
	var rec RecallOut
	call(t, cs, "recall", map[string]any{"query": "deploy mind-runner", "kinds": []string{"procedure"}}, &rec)
	if len(rec.Hits) != 1 || rec.Hits[0].Kind != "procedure" {
		t.Fatalf("hits=%+v", rec.Hits)
	}
}
```

- [ ] **Step 2: Chạy test — phải fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/brain/ ./internal/extract/ ./internal/server/ -run 'Procedure' -v`
Expected: FAIL — `kind không hợp lệ: "procedure"` (brain), `kind không hợp lệ "procedure"` (extract), `invalid kind "procedure"` (server).

- [ ] **Step 3: Implement**

`internal/brain/write.go` — thay dòng 35-40:

```go
// validKinds: kind hợp lệ (chữ thường) — D13: fact/preference/decision/procedure/
// document vĩnh viễn, note/task_hint/transcript/caption là sự kiện (purge theo retention).
var validKinds = map[string]bool{
	"note": true, "fact": true, "preference": true, "decision": true, "document": true,
	"procedure": true, "task_hint": true, "transcript": true, "caption": true,
}
```

`internal/extract/parse.go` — thay dòng 64-66:

```go
var extractKinds = map[string]bool{
	"fact": true, "preference": true, "decision": true, "note": true, "task_hint": true, "procedure": true,
}
```

`internal/extract/prompts/extract.md` — chèn sau bullet `preference` (dòng 17):

```markdown
- `procedure` — a repeatable way of doing something the user relies on ("how we deploy", "how I triage tickets"): the method, ordered steps or rules. Only when the transcript states the method; not a one-off event.
```

`internal/server/tools.go`:
- Dòng 31 (Kind desc) thành:
  `"decision (a choice made) | fact (stable truth about the user's world) | preference (how the user likes things) | procedure (a repeatable how-to) | note (event/observation, default) | task_hint (vague intention; use task_add for concrete to-dos)"`
- Dòng 67: `case "note", "fact", "preference", "decision", "task_hint", "procedure":`
- Dòng 69: `fmt.Errorf("invalid kind %q (note|fact|preference|decision|task_hint|procedure)", in.Kind)`
- Dòng 124 (RecallIn.Kinds desc):
  `"Restrict to kinds: decision, fact, preference, procedure, note, task_hint, document (ingested files), transcript (audio/video), caption (images). Omit to search everything."`
- Dòng 178: `fmt.Errorf("invalid kind %q (decision|fact|preference|procedure|note|task_hint|document|transcript|caption)", k)`

`internal/server/server.go` — chèn vào Instructions sau dòng 25 (`- How they like things done …`):

```
- A repeatable way of doing something ("how we do X", steps they follow) → kind=procedure.
```

`README.md` — chèn dòng vào bảng (sau dòng `preference`):

```markdown
| `procedure` | cách làm lặp lại (quy trình/các bước); không có trường phụ |
```

- [ ] **Step 4: Chạy test — pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/brain/ ./internal/extract/ ./internal/server/ -run 'Procedure' -v`
Expected: PASS cả 3.

- [ ] **Step 5: Full suite**

Run: `PATH=/usr/local/go/bin:$PATH go test ./...`
Expected: PASS toàn bộ.

---

### Task 2: `store.HardDeleteNotes` + purge sweep + UpsertNote hồi sinh

**Files:**
- Modify: `internal/store/purge.go` (PurgeReport dòng 17-20; purgeTx dòng 84-199)
- Modify: `internal/store/notes.go` (UpsertNote dòng 93-104)
- Modify: `internal/cli/maintenance.go` (dòng 164-165)
- Create: `internal/store/harddelete_test.go`
- Modify: `internal/store/purge_test.go`, `internal/store/structured_test.go`

**Interfaces:**
- Consumes: `purgeConn` (interface đã có trong purge.go), `noteAt`/`openMigrated` helpers test, `InsertRelation(ctx, spaceID, from, to, typ, *sourceNoteID, at)`.
- Produces: `func (s *Store) HardDeleteNotes(ctx context.Context, ids []int64) (int, error)` — Task 3 (remember/extract) và Task 5 (consolidate) gọi; `deleteNotesChain(ctx, purgeConn, ids) (relations, notes int, err error)` — dùng chung với purgeTx; `PurgeReport.Superseded` — maintenance in `superseded=%d`. `SupersedeNote` vẫn tồn tại đến hết Task 3.

- [ ] **Step 1: Viết test fail**

`internal/store/harddelete_test.go` (mới):

```go
package store

import (
	"context"
	"testing"
	"time"
)

// TestHardDeleteNotes: xoá cứng đủ chuỗi (relations, chunks, embeddings,
// chunks_fts, notes) trong một tx; note ngoài danh sách không bị đụng; chạy
// lại trên id đã xoá → 0, không lỗi.
func TestHardDeleteNotes(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	kill, _, err := s.UpsertNote(ctx, noteAt(1, "decision", "quyết định cũ", t0))
	if err != nil {
		t.Fatal(err)
	}
	keep, _, err := s.UpsertNote(ctx, noteAt(1, "decision", "quyết định mới", t0))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{kill, keep} {
		if err := s.InsertChunks(ctx, id, []Chunk{{Ordinal: 0, Text: "chunk", TokenCount: 1}}); err != nil {
			t.Fatal(err)
		}
		var chunkID int64
		if err := s.DB().QueryRowContext(ctx, `SELECT id FROM chunks WHERE note_id=?`, id).Scan(&chunkID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO embeddings(chunk_id, model, dim, vec, created_at) VALUES(?,?,?,?,?)`,
			chunkID, "m", 2, make([]byte, 8), ts(t0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.InsertRelation(ctx, 1, "A", "B", "related", &kill, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRelation(ctx, 1, "C", "D", "related", &keep, t0); err != nil {
		t.Fatal(err)
	}

	n, err := s.HardDeleteNotes(ctx, []int64{kill})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := s.FetchNote(ctx, kill); err == nil {
		t.Fatal("note chưa bị xoá")
	}
	counts := map[string]int{}
	for k, q := range map[string]string{
		"chunks":     `SELECT COUNT(*) FROM chunks`,
		"embeddings": `SELECT COUNT(*) FROM embeddings`,
		"fts":        `SELECT COUNT(*) FROM chunks_fts`,
		"relations":  `SELECT COUNT(*) FROM relations`,
	} {
		var cnt int
		if err := s.DB().QueryRowContext(ctx, q).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		counts[k] = cnt
	}
	if counts["chunks"] != 1 || counts["embeddings"] != 1 || counts["fts"] != 1 || counts["relations"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	if _, err := s.FetchNote(ctx, keep); err != nil {
		t.Fatalf("note giữ bị đụng: %v", err)
	}
	n2, err := s.HardDeleteNotes(ctx, []int64{kill})
	if err != nil || n2 != 0 {
		t.Fatalf("chạy lại: n=%d err=%v", n2, err)
	}
}
```

`internal/store/purge_test.go` — thêm fixture legacy sau khối (b) (sau vòng `for _, kind := range []string{"fact", "preference", "decision"}`):

```go
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
```

Sửa `want` (dòng 162):

```go
	want := PurgeReport{Events: 2, Superseded: 1, Episodes: 1, Relations: 2, Tasks: 1, Raws: 1, Jobs: 2, MediaRows: 3, MediaFiles: 1}
```

Kiểm tra legacy đã xoá — thêm cạnh dòng 185-187 (sau khối fetch `fgID`):

```go
	if _, err := s.FetchNote(ctx, spID); err == nil {
		t.Fatal("note superseded legacy vẫn còn")
	}
```

(`nNotes != 5` giữ nguyên: tạo thêm 1, xoá thêm 1. `chunks/embeddings/fts` vẫn phải 0 sau purge.)

`internal/store/structured_test.go` — thay toàn bộ `TestNoteMetaMergeAndSupersede` (dòng 85-140) bằng:

```go
// TestNoteMetaMergeAndRevive: ghi lại cùng text kèm meta mới → merge, không
// trùng; row legacy status='superseded' ghi lại y hệt → hồi sinh active (nội
// dung không mất trong cửa sổ trước khi purge quét).
func TestNoteMetaMergeAndRevive(t *testing.T) {
	st := openMigrated(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

	old := &Note{SpaceID: 1, Kind: "decision", Text: "Dùng Postgres", Source: "mcp", Tags: []string{"db"},
		Meta: NoteMeta{Why: "team quen"}, CreatedAt: now, UpdatedAt: now}
	oldID, _, err := st.UpsertNote(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	again := &Note{SpaceID: 1, Kind: "decision", Text: "Dùng Postgres", Source: "mcp", Tags: []string{"infra"},
		Meta: NoteMeta{Alternatives: []string{"MySQL"}}, CreatedAt: now, UpdatedAt: now.Add(time.Minute)}
	againID, fresh, err := st.UpsertNote(ctx, again)
	if err != nil || fresh || againID != oldID {
		t.Fatalf("merge: id=%d fresh=%v err=%v", againID, fresh, err)
	}
	n, err := st.FetchNote(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Meta.Why != "team quen" || len(n.Meta.Alternatives) != 1 || len(n.Tags) != 2 || n.Status != "active" {
		t.Fatalf("sau merge: meta=%+v tags=%v status=%q", n.Meta, n.Tags, n.Status)
	}

	// legacy: đánh dấu superseded bằng SQL tay (cơ chế cũ đã bỏ) rồi ghi lại
	// y hệt → hồi sinh active, không mất nội dung.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE notes SET status='superseded', superseded_by=? WHERE id=?`, oldID, oldID); err != nil {
		t.Fatal(err)
	}
	gID, fresh, err := st.UpsertNote(ctx, again)
	if err != nil || fresh || gID != oldID {
		t.Fatalf("hồi sinh: id=%d fresh=%v err=%v", gID, fresh, err)
	}
	n, err = st.FetchNote(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != "active" || n.SupersededBy != nil {
		t.Fatalf("hồi sinh: status=%q superseded_by=%v", n.Status, n.SupersededBy)
	}
}
```

Xoá import `errors` khỏi file (chỉ test cũ dùng).

- [ ] **Step 2: Chạy test — phải fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/store/ -run 'HardDelete|Purge|MergeAndRevive' -v`
Expected: FAIL build — `s.HardDeleteNotes undefined`, `unknown field Superseded in PurgeReport`.

- [ ] **Step 3: Implement**

`internal/store/purge.go` — PurgeReport (dòng 17-20):

```go
// PurgeReport đếm từng loại đã xoá (chạy lại lần 2 → toàn 0).
type PurgeReport struct {
	Events, Superseded, Episodes, Relations, Tasks, Raws, Jobs, MediaRows, MediaFiles int
}
```

`purgeTx` — thay khối (2)+(3) hiện tại (dòng 123-152) bằng:

```go
	// (1b) Legacy superseded (cơ chế cũ đã bỏ): mọi kind, mọi tuổi → quét sạch
	// một lần. Hiếm khi trùng id với tập (1) (superseded note sự kiện) — chỉ
	// lệch số đếm báo cáo, xoá vẫn đúng.
	rows, err = c.QueryContext(ctx, `SELECT id FROM notes WHERE deleted_at IS NULL AND status='superseded' ORDER BY id`)
	if err != nil {
		return PurgeReport{}, nil, err
	}
	var supIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return PurgeReport{}, nil, err
		}
		supIDs = append(supIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PurgeReport{}, nil, err
	}
	rep.Superseded = len(supIDs)
	ids = append(ids, supIDs...)

	// (2) Relations orphan cũ (không note nguồn); relations theo note nằm trong
	// chuỗi xoá ở bước (3).
	n, err := exec(`DELETE FROM relations WHERE source_note_id IS NULL AND created_at < ?`, evCut)
	if err != nil {
		return PurgeReport{}, nil, err
	}
	rep.Relations += n

	// (3) Chuỗi từng note: relations → embeddings → chunks (trigger dọn
	// chunks_fts) → notes.
	rel, _, err := deleteNotesChain(ctx, c, ids)
	if err != nil {
		return PurgeReport{}, nil, err
	}
	rep.Relations += rel
```

Thêm helper (sau `purgeTx`, cạnh `int64Args`):

```go
// deleteNotesChain xoá chuỗi của các note: relations (source_note_id) →
// embeddings → chunks (trigger chunks_ad dọn luôn chunks_fts) → notes.
// Một nguồn duy nhất cho purge và HardDeleteNotes.
func deleteNotesChain(ctx context.Context, c purgeConn, ids []int64) (relations, notes int, err error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	args := int64Args(ids)
	ph := placeholders(len(ids))
	res, err := c.ExecContext(ctx, `DELETE FROM relations WHERE source_note_id IN (`+ph+`)`, args...)
	if err != nil {
		return 0, 0, err
	}
	n1, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if _, err := c.ExecContext(ctx, `DELETE FROM embeddings WHERE chunk_id IN (SELECT id FROM chunks WHERE note_id IN (`+ph+`))`, args...); err != nil {
		return 0, 0, err
	}
	if _, err := c.ExecContext(ctx, `DELETE FROM chunks WHERE note_id IN (`+ph+`)`, args...); err != nil {
		return 0, 0, err
	}
	res, err = c.ExecContext(ctx, `DELETE FROM notes WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return 0, 0, err
	}
	n2, err := res.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	return int(n1), int(n2), nil
}
```

`internal/store/notes.go` — thêm `HardDeleteNotes` ngay trước `SupersedeNote` (dòng 230):

```go
// HardDeleteNotes xoá cứng cả chuỗi của các note (relations → embeddings →
// chunks → notes; trigger dọn chunks_fts) trong MỘT transaction BEGIN
// IMMEDIATE. Dùng khi thay thế ký ức (remember supersedes, dedupe lúc
// extract, consolidate). Không có xoá mềm ở đây — phục hồi chỉ từ backup
// ngày. Trả số note đã xoá; id không tồn tại → bỏ qua, không lỗi.
func (s *Store) HardDeleteNotes(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close() // rollback defer đăng ký sau → chạy trước
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	_, notes, err := deleteNotesChain(ctx, conn, ids)
	if err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, err
	}
	committed = true
	s.BumpGen()
	return notes, nil
}
```

`UpsertNote` — thay comment dòng 93-94 + SQL dòng 95-104:

```go
	// Ghi lại: meta merge (trường mới ghi đè, trường cũ giữ), tags hợp nhất
	// không trùng, và row legacy status='superseded' được HỒI SINH về active —
	// ghi lại y hệt không bao giờ làm mất nội dung (purge quét sạch legacy sau).
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO notes(space_id,kind,text,tags,source,session_id,content_hash,created_at,updated_at,meta,project)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(space_id, content_hash)
		 DO UPDATE SET updated_at=excluded.updated_at, deleted_at=NULL,
		   status='active', superseded_by=NULL,
		   project=COALESCE(notes.project, excluded.project),
		   meta=json_patch(notes.meta, excluded.meta),
		   tags=(SELECT json_group_array(value) FROM (
		     SELECT value FROM json_each(notes.tags) UNION SELECT value FROM json_each(excluded.tags)))
		 RETURNING id, created_at = updated_at`,
		n.SpaceID, n.Kind, n.Text, tags, n.Source, sessionID, n.ContentHash,
		ts(n.CreatedAt), ts(n.UpdatedAt), meta, nullStr(n.Project)).Scan(&id, &fresh)
```

`internal/cli/maintenance.go` — dòng 164-165:

```go
	fmt.Fprintf(stdout, "purge: events=%d superseded=%d episodes=%d relations=%d tasks=%d raws=%d jobs=%d media_rows=%d media_files=%d\n",
		rep.Events, rep.Superseded, rep.Episodes, rep.Relations, rep.Tasks, rep.Raws, rep.Jobs, rep.MediaRows, rep.MediaFiles)
```

- [ ] **Step 4: Chạy test — pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/store/ ./internal/cli/ -v`
Expected: PASS (bao gồm `TestMaintenancePurgesExpiredEvents` — Contains `"purge: events=1"` vẫn khớp vì `superseded=` nằm sau `events=%d`).

- [ ] **Step 5: Full suite**

Run: `PATH=/usr/local/go/bin:$PATH go test ./...`
Expected: PASS.

---

### Task 3: Bỏ hẳn supersede (xoá cứng mọi nơi)

**Files:**
- Modify: `internal/server/tools.go` (Supersedes 31-43, handler 80-115, RecallIn 127, HitOut 148-149, handler 182-189/215-220, forget desc 637, remember desc 58)
- Modify: `internal/server/server.go` (Instructions 23, 34)
- Modify: `internal/brain/briefing.go` (355-357)
- Modify: `internal/brain/recall.go` (32, 52-53, 150-151, 187-188, 380-381)
- Modify: `internal/store/search_fts.go` (30-36, 39-52, 55-60)
- Modify: `internal/store/notes.go` (xoá `SupersedeNote` 230-253; comment struct 30)
- Modify: `internal/extract/dedupe.go` (rename + comment + knowledgeKinds 41)
- Modify: `internal/extract/extract.go` (155, 178-183, 210-213)
- Modify: `README.md` (49, 70)
- Test: `internal/extract/dedupe_test.go`, `internal/extract/accuracy_test.go`, `internal/server/structured_test.go`

**Interfaces:**
- Consumes: `HardDeleteNotes` (Task 2).
- Produces: không còn `SupersedeNote`, `IncludeSuperseded`, `plan.supersede`, `supersedeThreshold`, `supersedeCandidates`; `plan.replaces []int64` + `replaceThreshold` + `replaceCandidates` là tên mới; mọi thay thế note = xoá cứng. `planDedupe` giữ chữ ký và semantics, chỉ đổi tên field.

- [ ] **Step 1: Sửa test theo semantics mới — phải fail (compile)**

`internal/extract/dedupe_test.go` — thay comment dòng 15-17 và các dòng 74-78:

```go
// TestPlanDedupeSameOpening: fact "kể lại" cùng một chuyện nhưng câu chữ khác
// (cosine dưới ngưỡng 0.92) vẫn bị nhận diện qua phần mở đầu trùng → thay thế
// (note cũ sẽ bị xoá cứng); fact khác chủ đề và note kind "note" thì không đụng gì.
```

```go
	if plan.replaces[0] != old.NoteID {
		t.Fatalf("fact kể lại phải thay thế note #%d: %v", old.NoteID, plan.replaces)
	}
	if plan.replaces[1] != 0 || plan.replaces[2] != 0 {
		t.Fatalf("chủ đề khác / kind note không được thay thế: %v", plan.replaces)
	}
```

`internal/extract/accuracy_test.go` — thay dòng 72-75:

```go
	// quyết định cũ bị xoá cứng (không còn trong DB)
	if _, err := st.FetchNote(ctx, old.NoteID); err == nil {
		t.Fatal("quyết định cũ phải bị xoá cứng")
	}
```

và comment dòng 28: `// TestRunSessionAccuracy: (1) quyết định gần trùng thay thế (xoá cứng) quyết định cũ,`

`internal/server/structured_test.go` — thay `TestStructuredRememberRecall` (dòng 43-86) bằng:

```go
// TestStructuredRememberRecall: decision kèm why/alternatives; supersedes XOÁ
// CỨNG quyết định cũ (recall chỉ còn note mới; forget note cũ → not found);
// kinds lọc đúng loại; ngày mơ hồ bị từ chối.
func TestStructuredRememberRecall(t *testing.T) {
	cs, done := connectTest(t)
	defer done()

	var old RememberOut
	call(t, cs, "remember", map[string]any{"text": "Dùng Postgres cho kho dữ liệu", "kind": "decision", "why": "team quen"}, &old)
	var nw RememberOut
	res := call(t, cs, "remember", map[string]any{
		"text": "Dùng SQLite cho kho dữ liệu", "kind": "decision", "why": "chạy local",
		"alternatives": []string{"Postgres"}, "supersedes": old.NoteID,
	}, &nw)
	if res.IsError || nw.Superseded != old.NoteID {
		t.Fatalf("supersede: %+v %v", nw, res.Content)
	}
	call(t, cs, "remember", map[string]any{"text": "Kho dữ liệu nằm ở ~/data", "kind": "fact"}, nil)

	var out RecallOut
	call(t, cs, "recall", map[string]any{"query": "kho dữ liệu", "kinds": []string{"decision"}}, &out)
	if len(out.Hits) != 1 || out.Hits[0].NoteID != nw.NoteID || out.Hits[0].Why != "chạy local" ||
		len(out.Hits[0].Alternatives) != 1 {
		t.Fatalf("hits=%+v stages=%v", out.Hits, out.Stages)
	}

	// note cũ đã bị xoá cứng: forget phải báo không tìm thấy
	var fg ForgetOut
	call(t, cs, "forget", map[string]any{"kind": "note", "id": old.NoteID}, &fg)
	if fg.Forgotten {
		t.Fatalf("note cũ phải đã bị xoá: %+v", fg)
	}

	if r := call(t, cs, "remember", map[string]any{"text": "họp", "when": "12/03/2025"}, nil); !r.IsError {
		t.Fatal("ngày mơ hồ phải isError")
	}
	if r := call(t, cs, "recall", map[string]any{"query": "x", "kinds": []string{"bogus"}}, nil); !r.IsError {
		t.Fatal("kind sai phải isError")
	}
}
```

- [ ] **Step 2: Chạy — phải fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/extract/ ./internal/server/ -run 'Dedupe|Accuracy|Structured' -v`
Expected: FAIL build — `plan.supersede` không tồn tại (chưa rename), `include_superseded` bị từ chối.

- [ ] **Step 3: Store + recall read-side**

`internal/store/search_fts.go` — dòng 28-36:

```go
// NoteFilter: điều kiện lọc note dùng chung cho FTS và vector (alias bảng notes
// là n). Rỗng = mọi space, mọi tag, mọi kind; luôn chỉ note còn hiệu lực.
type NoteFilter struct {
	SpaceIDs []int64
	Tags     []string // AND — note phải mang đủ
	Kinds    []string // OR — rỗng = mọi kind
	Project  string   // "" = mọi project; khác rỗng = chỉ note của project này
}
```

Dòng 39-52 `Key()` — bỏ khối `if f.IncludeSuperseded`:

```go
// Key: chuỗi tất định đại diện filter (khoá cache vector).
func (f NoteFilter) Key() string {
	var b strings.Builder
	for _, id := range f.SpaceIDs {
		fmt.Fprintf(&b, "%d,", id)
	}
	b.WriteString("|" + strings.Join(f.Tags, "\x00") + "|" + strings.Join(f.Kinds, ","))
	if f.Project != "" {
		b.WriteString("|p=" + f.Project)
	}
	return b.String()
}
```

Dòng 54-60 `sql()`:

```go
// sql: mệnh đề AND (bắt đầu bằng " AND ...") + args; luôn loại note xoá mềm và
// note legacy status='superseded' (lưới an toàn tới khi purge quét hết).
func (f NoteFilter) sql() (string, []any) {
	q := ` AND n.deleted_at IS NULL AND n.status = 'active'`
	var args []any
```

`internal/brain/recall.go`:
- Bỏ dòng 32 (`IncludeSuperseded bool …`).
- `RecallHit` — bỏ dòng 52-53 (`Status`, `SupersededBy`).
- Dòng 150-151: `SpaceIDs: allIDs, Tags: p.Tags, Kinds: p.Kinds, Project: p.Project}, ftsN)`
- Dòng 187-188: `SpaceIDs: g.spaceIDs, Tags: p.Tags, Kinds: p.Kinds, Project: p.Project}, qv[0], vecN)`
- Khối dựng hit — bỏ `Status: n.Status,` và `SupersededBy: n.SupersededBy,` (dòng 380-381).

`internal/store/notes.go`:
- Xoá cả comment + hàm `SupersedeNote` (dòng 230-253). Giữ `ErrNoteNotFound`.
- Comment struct dòng 30-32:

```go
	// Status/SupersededBy: legacy — cơ chế supersede đã bỏ (mọi thay thế xoá
	// cứng); giữ cột để đọc row cũ tới khi purge (1b) quét sạch.
	Status       string
	SupersededBy *int64
```

`internal/brain/briefing.go` — dòng 353-359 bỏ marker:

```go
	for _, n := range notes {
		line := fmt.Sprintf("- note #%d [%s]: %s", n.ID, n.Kind, clipText(n.Text, 120)) + projectTag(n.Project, b.project)
		lines = append(lines, line)
	}
```

- [ ] **Step 4: Write-side (remember, extract dedupe)**

`internal/server/tools.go`:
- Dòng 40 Supersedes desc: `jsonschema:"note_id of an earlier decision/fact this one replaces (from recall); the old one is permanently deleted (recovery only via backups)"`
- Dòng 45-50 RememberOut:

```go
// RememberOut — kết quả ghi: created=false nghĩa là nội dung đã có (ghi lặp = ôn lại).
type RememberOut struct {
	NoteID  int64 `json:"note_id"`
	Created bool  `json:"created"`
	// Superseded: id note cũ đã XOÁ CỨNG (khi tool được gọi kèm supersedes).
	Superseded int64 `json:"superseded,omitempty"`
}
```

- Dòng 58 remember desc: `"If it replaces an earlier decision or fact, recall first and pass its note_id as supersedes (the old note is permanently deleted). " +`
- Dòng 110-115 handler:

```go
		out := RememberOut{NoteID: res.NoteID, Created: res.Fresh}
		if in.Supersedes != 0 && in.Supersedes != res.NoteID {
			if _, err := st.HardDeleteNotes(ctx, []int64{in.Supersedes}); err != nil {
				return nil, RememberOut{}, err
			}
			out.Superseded = in.Supersedes
		}
```

- `RecallIn` bỏ dòng 127 (`IncludeSuperseded …`).
- `HitOut` bỏ dòng 148-149 (`Status`, `SupersededBy`).
- Recall handler bỏ `IncludeSuperseded: in.IncludeSuperseded,` (dòng 187) và khối dòng 215-220 (`if h.Status …` / `if h.SupersededBy …`).
- Dòng 637 forget desc: `"For a decision that merely changed, use remember with supersedes instead — the old note is permanently deleted.",`

`internal/server/server.go`:
- Dòng 23: `- A choice was made → remember kind=decision, with why (and alternatives) if stated. If it replaces an earlier decision, recall it and pass supersedes=<note_id> — the old note is permanently deleted.`
- Dòng 34 đoạn "mind fix:": `"mind fix:" → correct memory (task_update for a task #id, or remember with supersedes for a note — the old note is permanently deleted); "mind forget:" → forget (confirm what will be removed first).`

`internal/extract/dedupe.go` — thay comment đầu file + consts + plan:

```go
// Ngưỡng cosine (embedding) để coi hai note là cùng một ý.
//   - replaceThreshold: kiến thức (decision/fact/preference/procedure) gần giống
//     note đang active cùng kind → note mới thay thế note cũ (cũ bị XOÁ CỨNG —
//     phục hồi chỉ từ backup ngày). Thông tin mới nhất thắng — đúng cả khi người
//     dùng đổi ý ("dùng SQLite" → "dùng Postgres") vì hai câu đó cũng rất gần.
//   - batchDupThreshold: hai note trong cùng một lần trích gần như y hệt
//     (hay gặp khi nhiều cửa sổ) → bỏ note sau.
//
// Câu dài kể lại cùng một chuyện bằng chữ khác thì cosine bị pha loãng (đuôi
// câu khác nhau) — bắt thêm bằng sameOpening: top-N gần nhất, note nào có
// phần mở đầu trùng khớp sau chuẩn hoá cũng bị thay thế.
//
// Sự kiện (note/task_hint) không thay thế nhau: "deploy v0.1.2" và "deploy
// v0.1.3" rất gần nhưng là hai sự kiện khác nhau.
var (
	replaceThreshold  = 0.92
	batchDupThreshold = 0.95
)

// replaceCandidates: số note gần nhất (cùng kind) đem đi so phần mở đầu khi
// cosine không đủ ngưỡng; sharedOpeningRunes: số rune đầu (chuẩn hoá) tối
// thiểu phải trùng để coi là kể lại cùng một chuyện.
// ponytail: prefix 40 rune là heuristic — nâng lên so token nếu bắt hụt.
const (
	replaceCandidates  = 5
	sharedOpeningRunes = 40
)

var knowledgeKinds = map[string]bool{"decision": true, "fact": true, "preference": true, "procedure": true}

// dedupePlan: kết quả dò trùng cho từng note của một lần trích.
type dedupePlan struct {
	skip     []bool  // trùng note khác trong cùng lần trích → không ghi
	replaces []int64 // note cũ (đang active) sẽ bị xoá cứng khi ghi note này; 0 = không
}
```

Trong `planDedupe`: `plan := dedupePlan{skip: make([]bool, len(notes)), replaces: make([]int64, len(notes))}`; `VectorTop(…, replaceCandidates)`; `hits[0].Score >= replaceThreshold`; `plan.replaces[i] = …` (cả 2 chỗ).

`internal/extract/extract.go`:
- Dòng 155: `var stat struct{ skipped, deleted, updated, badIDs int }`
- Dòng 178-183:

```go
		if old := plan.replaces[i]; old != 0 && old != res.NoteID {
			if _, err := x.st.HardDeleteNotes(ctx, []int64{old}); err != nil {
				return fmt.Errorf("notes[%d] xoá note cũ %d: %w", i, old, err)
			}
			stat.deleted++
		}
```

- Dòng 210-213:

```go
	if lg != nil && (stat.skipped+stat.deleted+stat.badIDs > 0) {
		// chỉ số đo (không nội dung) — để chỉnh ngưỡng dò trùng
		lg.Info("extract: dò trùng", "notes", len(ex.Notes), "skipped", stat.skipped,
			"deleted", stat.deleted, "task_updates", stat.updated, "bad_task_ids", stat.badIDs)
	}
```

`README.md`:
- Dòng 49: `| \`decision\` | \`why\`, \`alternatives\`, \`who\`, \`when\`; \`supersedes\` xoá cứng quyết định cũ (phục hồi chỉ từ backup) |`
- Dòng 70: `Recall mặc định trả 5 kết quả, bỏ hit có điểm rerank dưới 0.2 và luôn ẩn note đã xoá/đã quên; lọc được theo \`kinds\`. Chỉnh trong config:`

- [ ] **Step 5: Grep xác nhận chỉ còn chỗ đúng**

Run: `grep -rn "SupersedeNote\|IncludeSuperseded\|plan\.supersede\|supersedeThreshold\|supersedeCandidates" internal/ --include='*.go'`
Expected: không kết quả. Run tiếp: `grep -rn "superseded" internal/ --include='*.go'`
Expected còn đúng các chỗ: `store/notes.go` (comment legacy + SQL hồi sinh), `store/purge.go` (query sweep), `store/migrations/*.sql` (lịch sử — không sửa).

- [ ] **Step 6: Chạy test — pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./...`
Expected: PASS toàn bộ.

---

### Task 4: Config `[consolidate]` + maintenance enqueue

**Files:**
- Modify: `internal/config/config.go` (struct 14-30, `Consolidate` type mới, Default 123-138)
- Modify: `internal/config/config_test.go`
- Modify: `internal/cli/maintenance.go` (const, bước (3b), helper `consolidateDue`, doc comment 34-37)
- Modify: `README.md` (khối `[recall]` 72-76)
- Modify: `docs/superpowers/specs/2026-10-09-procedure-consolidation-design.md` (vị trí bước enqueue)
- Modify: `e2e/e2e_test.go` (Phát hiện khi chạy: assert "Queue sạch" sau maintenance #1 giờ phải trừ `consolidate` — job tuần nằm chờ sweep MCP có key theo đúng thiết kế; thêm assert đúng 1 job consolidate queued.)
- Create: `internal/cli/consolidate_test.go`

**Interfaces:**
- Consumes: `st.GetMeta/SetMeta` (`(string, bool, error)` / `error`), `Enqueue(ctx, "consolidate", nil, now)` (nil payload → `'{}'`; idempotent theo type+payload còn queued/failed/running — chính là pending-check).
- Produces: `cfg.Consolidate.EveryDays` (default 7, ≤0 = tắt); `consolidateDue(last string, everyDays int, now time.Time) bool`; meta key `last_consolidate_enqueue` (RFC3339 UTC) — Task 5's cycle test dựa vào. Job `consolidate` chưa có handler đến hết Task 4 (không test nào chạy nó vì enqueue đặt SAU queue).

- [ ] **Step 1: Viết test fail**

`internal/config/config_test.go`:
- `sampleTOML` — thêm cuối chuỗi (sau `[backup]\nkeep = 7\n`):

```toml
[consolidate]
every_days = 30
```

- `TestFullTOMLParses` — thêm trước khối `if len(cfg.Warnings()) != 0`:

```go
	if cfg.Consolidate.EveryDays != 30 {
		t.Fatalf("Consolidate=%+v", cfg.Consolidate)
	}
```

- `TestMissingFileGivesDefaults` — thêm cạnh các assert hiện có:

```go
	if cfg.Consolidate.EveryDays != 7 {
		t.Fatalf("Consolidate=%+v", cfg.Consolidate)
	}
```

`internal/cli/consolidate_test.go` (mới):

```go
package cli

import (
	"testing"
	"time"
)

// TestConsolidateDue: chưa có mốc (lần đầu) hoặc mốc hỏng → chạy ngay; đủ
// every_days → chạy; chưa đủ → không.
func TestConsolidateDue(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		last string
		want bool
	}{
		{"", true}, // lần đầu
		{"rác", true},
		{now.AddDate(0, 0, -6).Format(time.RFC3339), false},
		{now.AddDate(0, 0, -7).Format(time.RFC3339), true},
	}
	for _, c := range cases {
		if got := consolidateDue(c.last, 7, now); got != c.want {
			t.Errorf("consolidateDue(%q)=%v, muốn %v", c.last, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Chạy — phải fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/config/ -run FullTOML -v` và `PATH=/usr/local/go/bin:$PATH go test ./internal/cli/ -run ConsolidateDue -v`
Expected: FAIL — field `Consolidate` không tồn tại; `consolidateDue undefined`.

- [ ] **Step 3: Implement**

`internal/config/config.go` — struct (dòng 14-24):

```go
type Config struct {
	DataDir     string      `toml:"data_dir"`
	LogLevel    string      `toml:"log_level"`
	Gateway     Gateway     `toml:"gateway"`
	Spaces      Spaces      `toml:"spaces"`
	Retention   Retention   `toml:"retention"`
	Briefing    Briefing    `toml:"briefing"`
	Recall      Recall      `toml:"recall"`
	Media       Media       `toml:"media"`
	Backup      Backup      `toml:"backup"`
	Consolidate Consolidate `toml:"consolidate"`
```

Thêm type (cạnh `Backup`, dòng 100-102):

```go
// Consolidate: job LLM định kỳ gộp note kiến thức trùng (decision/fact/
// preference/procedure). EveryDays <= 0 = tắt.
type Consolidate struct {
	EveryDays int `toml:"every_days"`
}
```

`Default()` (dòng 123-138) — thêm dòng sau `Backup: Backup{Keep: 7},`:

```go
		Consolidate: Consolidate{EveryDays: 7},
```

`internal/cli/maintenance.go`:
- Doc comment dòng 36-37:

```go
//	backfill embed → merge spool → [--retry-dead] retry-dead → queue →
//	consolidate enqueue → watch dirs → purge → wal_checkpoint(TRUNCATE)
//	→ VACUUM INTO + rotate → quick_check
```

- Const cạnh `freeSpaceFn` (dòng 25-32):

```go
// metaConsolidate: mốc enqueue consolidate gần nhất (RFC3339 UTC).
const metaConsolidate = "last_consolidate_enqueue"
```

- Bước mới ngay sau khối (3) queue (sau dòng 139, trước `// (3b) watch dirs`; đổi comment watch thành `(3c)`):

```go
	// (3b) consolidate: enqueue job LLM gộp note trùng định kỳ theo
	// [consolidate].every_days (0 = tắt). Đặt SAU queue: launchd chạy không key,
	// enqueue trước queue chỉ tổ bị claim rồi hoãn — job nằm chờ sweep MCP (có
	// key) hoặc maintenance lần sau. Meta đặt sau enqueue; lỗi giữa chừng →
	// lần sau thử lại. Enqueue idempotent → không bao giờ xếp trùng.
	if days := cfg.Consolidate.EveryDays; days > 0 {
		last, _, err := st.GetMeta(ctx, metaConsolidate)
		if err != nil {
			fmt.Fprintln(stderr, "maintenance: consolidate:", err)
			return 1
		}
		if consolidateDue(last, days, time.Now()) {
			if _, err := st.Enqueue(ctx, "consolidate", nil, time.Now()); err != nil {
				fmt.Fprintln(stderr, "maintenance: consolidate:", err)
				return 1
			}
			if err := st.SetMeta(ctx, metaConsolidate, time.Now().UTC().Format(time.RFC3339)); err != nil {
				fmt.Fprintln(stderr, "maintenance: consolidate:", err)
				return 1
			}
			fmt.Fprintf(stdout, "consolidate: enqueued (every_days=%d)\n", days)
		} else {
			fmt.Fprintln(stdout, "consolidate: chưa tới hạn")
		}
	}
```

- Helper cạnh `rotateBackups`:

```go
// consolidateDue: đến hạn chạy consolidate chưa — chưa có mốc (lần đầu) hoặc
// mốc hỏng → chạy ngay (thà chạy lại còn hơn tắt im lặng); ngược lại khi
// now >= mốc + everyDays.
func consolidateDue(last string, everyDays int, now time.Time) bool {
	if last == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	return !now.Before(t.AddDate(0, 0, everyDays))
}
```

`README.md` — thêm sau khối `[recall]` (dòng 72-76):

```toml
[consolidate]
every_days = 7   # định kỳ quét cả kho, gộp note kiến thức trùng (LLM); 0 = tắt
```

Spec: trong `docs/superpowers/specs/2026-10-09-procedure-consolidation-design.md`, sửa mục B2 — đổi "bước 2b (giữa retry-dead và queue)" thành "bước 3b (SAU queue, trước watch dirs)" + một câu lý do keyless như Global Constraints.

- [ ] **Step 4: Chạy — pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/config/ ./internal/cli/ -v`
Expected: PASS (maintenance tests cũ giữ nguyên kỳ vọng — job consolidate nằm lại queued, không ảnh hưởng `queue: processed`).

- [ ] **Step 5: Full suite**

Run: `PATH=/usr/local/go/bin:$PATH go test ./...`
Expected: PASS.

---

### Task 5: package `internal/consolidate` + đăng ký worker

> **Phát hiện khi chạy (2 sai khác có chủ đích):**
> 1. Batch tách thêm theo **kind** `(space, project, kind)`, không chỉ (space, project) như code dưới. Test `TestRunMerges` ("lần 2 không gọi model") và `validateMerges` (merge chỉ cùng kind) chỉ đúng khi batch cùng kind; batch trộn kind là nhiễu thuần cho model. Hàm `filterProject` → `filterProjectKind`.
> 2. `TestMaintenanceConsolidationCycle` phải set `cfg.Gateway.Models.Extract` + `writeConfig` trước khi chạy — `egress.Chat` cần model extract ("cloud thiếu model extract"), `setupFresh` không set model nào.

**Files:**
- Create: `internal/consolidate/consolidate.go`, `internal/consolidate/prompts/consolidate.md`, `internal/consolidate/consolidate_test.go`
- Modify: `internal/store/notes.go` (thêm `ActiveNotesByKinds` cạnh `NotesByKind` 198-225)
- Modify: `internal/worker/register.go`
- Modify: `internal/cli/maintenance_test.go` (test cycle)

**Interfaces:**
- Consumes: `store.ActiveNotesByKinds(ctx, spaceID, kinds)` (mới); `brain.WriteNote`; `store.HardDeleteNotes` (Task 2); `egress.Chat(ctx, pol, system, user) (string, error)`; `egressfake` (Options.ChatResp); meta `last_consolidate_enqueue` + bước (3b) (Task 4); helpers cli test `setupFresh`, `seedNoteChunks`.
- Produces: `consolidate.New(st, b, eg, cfg) *Runner` + `(*Runner).Run(ctx) error`; job type `"consolidate"` (payload `'{}'`); `ActiveNotesByKinds`.

- [ ] **Step 1: Viết test fail**

`internal/consolidate/consolidate_test.go` (mới):

```go
package consolidate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/store"
)

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

func seed(t *testing.T, st *store.Store, kind, text string) int64 {
	t.Helper()
	now := time.Now()
	id, _, err := st.UpsertNote(context.Background(), &store.Note{
		SpaceID: 1, Kind: kind, Text: text, Source: "tool:remember", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestRunMerges: model gộp 2 fact trùng → note mới (source consolidate, tag
// hợp nhất), note cũ bị xoá cứng; note không trùng không đụng; lần 2 (còn 1
// fact) không gọi model.
func TestRunMerges(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := seed(t, st, "fact", "Server chạy ở cổng 8080")
	b := seed(t, st, "fact", "Cổng của server là 8080")
	keep := seed(t, st, "preference", "Thích cà phê đen")
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE notes SET tags='["infra"]' WHERE id=?`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE notes SET tags='["server"]' WHERE id=?`, b); err != nil {
		t.Fatal(err)
	}

	fake := egressfake.New(t, egressfake.Options{
		ChatResp: func(_, _ string) string {
			return fmt.Sprintf(`{"merges":[{"kind":"fact","text":"Server của dịch vụ chạy ở cổng 8080.","notes":[%d,%d]}]}`, a, b)
		},
	})
	cfg := egressfake.Config(fake, nil)
	r := New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := st.FetchNote(ctx, a); err == nil {
		t.Fatal("note cũ a chưa bị xoá")
	}
	if _, err := st.FetchNote(ctx, b); err == nil {
		t.Fatal("note cũ b chưa bị xoá")
	}
	facts, err := st.ActiveNotesByKinds(ctx, 1, []string{"fact"})
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	m := facts[0]
	if m.Source != "consolidate" || m.Text != "Server của dịch vụ chạy ở cổng 8080." ||
		len(m.Tags) != 2 {
		t.Fatalf("note gộp=%+v", m)
	}
	if _, err := st.FetchNote(ctx, keep); err != nil {
		t.Fatalf("preference bị đụng: %v", err)
	}

	// lần 2: chỉ còn 1 fact → không có batch ≥2 → không thêm lượt gọi model
	calls := fake.ChatCalls
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.ChatCalls != calls {
		t.Fatalf("lần 2 không được gọi model: %d → %d", calls, fake.ChatCalls)
	}
}

// TestRunGuardSkipsMassDelete: batch 10 note, model đòi gộp 7 → xoá 6 = 60% >
// 50% → bỏ nguyên batch, không note nào mất.
func TestRunGuardSkipsMassDelete(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ids := make([]int64, 0, 10)
	for i := 0; i < 10; i++ {
		ids = append(ids, seed(t, st, "fact", fmt.Sprintf("sự thật số %d", i)))
	}
	fake := egressfake.New(t, egressfake.Options{
		ChatResp: func(_, _ string) string {
			parts := make([]string, 7)
			for i := 0; i < 7; i++ {
				parts[i] = fmt.Sprint(ids[i])
			}
			return `{"merges":[{"kind":"fact","text":"sự thật gộp","notes":[` + strings.Join(parts, ",") + `]}]}`
		},
	})
	cfg := egressfake.Config(fake, nil)
	r := New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.ActiveNotesByKinds(ctx, 1, []string{"fact"})
	if err != nil || len(got) != 10 {
		t.Fatalf("guard không chặn: còn %d note err=%v", len(got), err)
	}
}

// TestRunRejectsInvalidMerges: merge sai (kind ngoài tầng kiến thức, id ngoài
// batch, <2 note, JSON hỏng) → lỗi, không note nào bị xoá.
func TestRunRejectsInvalidMerges(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := seed(t, st, "fact", "sự thật một")
	b := seed(t, st, "fact", "sự thật hai")
	c := seed(t, st, "fact", "sự thật ba")

	var resp string
	fake := egressfake.New(t, egressfake.Options{ChatResp: func(_, _ string) string { return resp }})
	cfg := egressfake.Config(fake, nil)
	r := New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)

	cases := []struct {
		name, in string
	}{
		{"kind ngoài kiến thức", `{"merges":[{"kind":"note","text":"x","notes":[` + fmt.Sprint(a) + `,` + fmt.Sprint(b) + `]}]}`},
		{"id ngoài batch", `{"merges":[{"kind":"fact","text":"x","notes":[` + fmt.Sprint(a) + `,99999]}]}`},
		{"thiếu note", `{"merges":[{"kind":"fact","text":"x","notes":[` + fmt.Sprint(a) + `]}]}`},
		{"JSON hỏng", `{"merges":[`},
	}
	for _, tc := range cases {
		resp = tc.in
		if err := r.Run(ctx); err == nil {
			t.Fatalf("%s: muốn lỗi, nhận nil", tc.name)
		}
		for _, id := range []int64{a, b, c} {
			if _, err := st.FetchNote(ctx, id); err != nil {
				t.Fatalf("%s: note %d bị xoá dù lỗi: %v", tc.name, id, err)
			}
		}
	}
}
```

`internal/cli/maintenance_test.go` — thêm cuối file:

```go
// TestMaintenanceConsolidationCycle: lần chạy 1 enqueue consolidate (due ngay,
// nằm sau queue nên chưa chạy); lần chạy 2 queue chạy job → model gộp 2 fact
// trùng, note cũ bị xoá cứng; lần chạy 3 chưa tới hạn → không enqueue mới.
func TestMaintenanceConsolidationCycle(t *testing.T) {
	_, dataDir := setupFresh(t)
	var mergeResp string
	fake := egressfake.New(t, egressfake.Options{
		ChatResp: func(_, _ string) string { return mergeResp },
	})
	t.Setenv("MIND_RUNNER_GATEWAY_BASE_URL", fake.URL)

	a := seedNoteChunks(t, dataDir, "Server chạy ở cổng 8080")
	b := seedNoteChunks(t, dataDir, "Cổng server là 8080")
	// đổi kind sang fact (tầng kiến thức) để consolidate thấy
	st, err := store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE notes SET kind='fact' WHERE id IN (?,?)`, a, b); err != nil {
		t.Fatal(err)
	}
	st.Close()
	mergeResp = fmt.Sprintf(`{"merges":[{"kind":"fact","text":"Server của dịch vụ chạy ở cổng 8080.","notes":[%d,%d]}]}`, a, b)

	var out, errb bytes.Buffer
	if code := RunMaintenance(nil, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("lần 1: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "consolidate: enqueued (every_days=7)") {
		t.Fatalf("lần 1 out=%s", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := RunMaintenance(nil, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("lần 2: exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "consolidate: chưa tới hạn") {
		t.Fatalf("lần 2 out=%s", out.String())
	}

	st, err = store.Open(filepath.Join(dataDir, "mind-runner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.FetchNote(ctx, a); err == nil {
		t.Fatal("note cũ a chưa bị xoá")
	}
	if _, err := st.FetchNote(ctx, b); err == nil {
		t.Fatal("note cũ b chưa bị xoá")
	}
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notes WHERE kind='fact' AND deleted_at IS NULL AND text LIKE 'Server của dịch vụ%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("note gộp=%d, muốn 1", n)
	}
	var done int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE type='consolidate' AND state='queued'`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != 0 {
		t.Fatalf("còn %d job consolidate queued", done)
	}
}
```

Thêm import `"fmt"` vào `maintenance_test.go`.

- [ ] **Step 2: Chạy — phải fail**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/consolidate/ ./internal/cli/ -run 'Consolid|Consolidation' -v`
Expected: FAIL — package `consolidate` chưa tồn tại; `ActiveNotesByKinds undefined`.

- [ ] **Step 3: Implement**

`internal/store/notes.go` — thêm sau `NotesByKind` (dòng 225):

```go
// ActiveNotesByKinds: note chưa xoá mềm, status active, kind trong danh sách
// của space; sắp theo id (thứ tự tất định cho batch LLM của consolidate).
func (s *Store) ActiveNotesByKinds(ctx context.Context, spaceID int64, kinds []string) ([]Note, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	q := `SELECT ` + noteCols + ` FROM notes
	      WHERE space_id=? AND deleted_at IS NULL AND status='active' AND kind IN (` + placeholders(len(kinds)) + `)
	      ORDER BY id`
	args := []any{spaceID}
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := s.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
```

`internal/consolidate/consolidate.go` (mới):

```go
// Package consolidate: job định kỳ quét note kiến thức (decision/fact/
// preference/procedure) từng space, gom theo project rồi gửi từng batch cho
// model tìm note trùng/lặp và gộp thành một note mới; note cũ bị XOÁ CỨNG.
// Kết quả chỉ đi qua WriteNote + HardDeleteNotes (không đụng bảng khác).
package consolidate

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress"
	"mind-runner/internal/store"
)

//go:embed prompts/consolidate.md
var consolidatePrompt string

// knowledgeKinds: tầng kiến thức (vĩnh viễn) — cùng tập với dedupe của extract.
var knowledgeKinds = []string{"decision", "fact", "preference", "procedure"}

// maxBatchRunes: trần rune một batch gửi model (cắt theo dòng note); 20k rune
// ≈ 6-8k token, an toàn cho cả model local.
const maxBatchRunes = 20_000

// minGuardBatch: guard "xoá > 50% batch" chỉ tin được từ 8 note trở lên —
// batch 2-7 note gộp cả batch là chuyện thường (kho thưa). ponytail: heuristic,
// nới khi thấy chặn nhầm.
const minGuardBatch = 8

// Runner chạy job consolidate.
type Runner struct {
	st  *store.Store
	b   *brain.Brain
	eg  *egress.Egress
	cfg *config.Config
}

func New(st *store.Store, b *brain.Brain, eg *egress.Egress, cfg *config.Config) *Runner {
	return &Runner{st: st, b: b, eg: eg, cfg: cfg}
}

// merge một nhóm note model trả về.
type merge struct {
	Kind  string  `json:"kind"`
	Text  string  `json:"text"`
	Notes []int64 `json:"notes"`
}

// Run quét toàn kho theo space → project → batch; gộp note trùng, xoá cứng
// note cũ. Lỗi model/parse/validate → trả lỗi để job retry (nhất quán với
// extract: ồn ào, không bỏ qua lặng lẽ); batch đã áp dụng giữ nguyên — retry
// tính lại từ trạng thái hiện tại, ghi trùng là idempotent.
// ponytail: chạy trên một job, không heartbeat — vượt StaleRunning (30') có
// thể bị process khác chạy lại; hậu quả tự lành (gộp lần 2 thành no-op).
func (r *Runner) Run(ctx context.Context) error {
	sps, err := r.st.Spaces(ctx)
	if err != nil {
		return err
	}
	var stat struct{ batches, merges, deleted, guardSkipped int }
	for _, sp := range sps {
		notes, err := r.st.ActiveNotesByKinds(ctx, sp.ID, knowledgeKinds)
		if err != nil {
			return err
		}
		pol := egress.Policy(r.cfg.SpacePolicy(sp.Name))
		for _, proj := range projectsOf(notes) {
			for _, batch := range packBatches(filterProject(notes, proj), maxBatchRunes) {
				if len(batch) < 2 {
					continue
				}
				stat.batches++
				merges, err := r.mergeBatch(ctx, pol, batch)
				if err != nil {
					return err
				}
				if len(merges) == 0 {
					continue
				}
				if guardSkip(batch, merges) {
					stat.guardSkipped++
					continue
				}
				for _, m := range merges {
					del, err := r.applyMerge(ctx, sp.ID, proj, m, batch)
					if err != nil {
						return err
					}
					stat.merges++
					stat.deleted += del
				}
			}
		}
	}
	if lg := r.eg.UsageLog(); lg != nil && (stat.merges > 0 || stat.guardSkipped > 0) {
		// chỉ số đo, không nội dung
		lg.Info("consolidate: xong", "batches", stat.batches, "merges", stat.merges,
			"deleted", stat.deleted, "guard_skipped", stat.guardSkipped)
	}
	return nil
}

// mergeBatch gửi một batch cho model, parse + validate kết quả.
func (r *Runner) mergeBatch(ctx context.Context, pol egress.Policy, batch []store.Note) ([]merge, error) {
	var bldr strings.Builder
	for _, n := range batch {
		bldr.WriteString(noteLine(n))
		bldr.WriteByte('\n')
	}
	out, err := r.eg.Chat(ctx, pol, consolidatePrompt, bldr.String())
	if err != nil {
		return nil, err
	}
	ms, err := parseMerges(out)
	if err != nil {
		return nil, err
	}
	if err := validateMerges(ms, batch); err != nil {
		return nil, err
	}
	return ms, nil
}

// noteLine: một dòng cho model — "#id [kind] (YYYY-MM-DD) tags: text"; ngày là
// updated_at (bản kể mới nhất thường mới nhất).
func noteLine(n store.Note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d [%s] (%s)", n.ID, n.Kind, n.UpdatedAt.Format("2006-01-02"))
	if len(n.Tags) > 0 {
		b.WriteString(" " + strings.Join(n.Tags, ","))
	}
	b.WriteString(": " + n.Text)
	return b.String()
}

// parseMerges đọc output model: lấy đoạn '{'…'}' (bỏ fence/lời dẫn), unmarshal.
func parseMerges(s string) ([]merge, error) {
	s = strings.TrimSpace(s)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var out struct {
		Merges []merge `json:"merges"`
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("JSON không hợp lệ: %w", err)
	}
	return out.Merges, nil
}

// validateMerges: chặt, vi phạm → lỗi (job retry, không bỏ qua lặng lẽ).
func validateMerges(ms []merge, batch []store.Note) error {
	inBatch := make(map[int64]store.Note, len(batch))
	for _, n := range batch {
		inBatch[n.ID] = n
	}
	used := map[int64]bool{}
	for i, m := range ms {
		ok := false
		for _, k := range knowledgeKinds {
			if k == m.Kind {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("merges[%d]: kind %q không thuộc tầng kiến thức", i, m.Kind)
		}
		if strings.TrimSpace(m.Text) == "" {
			return fmt.Errorf("merges[%d]: text rỗng", i)
		}
		if len(m.Notes) < 2 {
			return fmt.Errorf("merges[%d]: cần ≥2 note", i)
		}
		seen := map[int64]bool{}
		for _, id := range m.Notes {
			n, in := inBatch[id]
			if !in {
				return fmt.Errorf("merges[%d]: note #%d không thuộc batch", i, id)
			}
			if seen[id] {
				return fmt.Errorf("merges[%d]: note #%d lặp", i, id)
			}
			seen[id] = true
			if used[id] {
				return fmt.Errorf("merges[%d]: note #%d đã dùng ở merge khác", i, id)
			}
			used[id] = true
			if n.Kind != m.Kind {
				return fmt.Errorf("merges[%d]: kind %q khác kind note #%d (%s)", i, m.Kind, id, n.Kind)
			}
		}
	}
	return nil
}

// guardSkip: model đòi xoá > nửa batch (từ 8 note) → nghi ngờ, bỏ nguyên batch.
// deletions = tổng note mất đi = Σ(len(notes)-1) (mỗi merge giữ lại 1 note kết quả).
func guardSkip(batch []store.Note, ms []merge) bool {
	if len(batch) < minGuardBatch {
		return false
	}
	deletions := 0
	for _, m := range ms {
		deletions += len(m.Notes) - 1
	}
	return deletions*2 > len(batch)
}

// applyMerge ghi note gộp rồi xoá cứng note cũ (trừ chính note vừa ghi — nội
// dung trùng một note cũ thì UpsertNote trả về id note đó). Tag = hợp nhất tag
// các note cũ (giữ đường lọc quen thuộc). Trả số note đã xoá.
func (r *Runner) applyMerge(ctx context.Context, spaceID int64, proj string, m merge, batch []store.Note) (int, error) {
	byID := make(map[int64]store.Note, len(batch))
	for _, n := range batch {
		byID[n.ID] = n
	}
	var tags []string
	seen := map[string]bool{}
	for _, id := range m.Notes {
		for _, t := range byID[id].Tags {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	res, err := r.b.WriteNote(ctx, brain.WriteParams{
		SpaceID: spaceID, Kind: m.Kind, Text: m.Text, Tags: tags,
		Source: "consolidate", Project: orNone(proj),
	})
	if err != nil {
		return 0, err
	}
	var old []int64
	for _, id := range m.Notes {
		if id != res.NoteID {
			old = append(old, id)
		}
	}
	if len(old) == 0 {
		return 0, nil
	}
	return r.st.HardDeleteNotes(ctx, old)
}

// projectsOf: danh sách project có note, sắp tất định. Batch không trộn hai
// project — note gộp phải thuộc một project.
func projectsOf(notes []store.Note) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range notes {
		if !seen[n.Project] {
			seen[n.Project] = true
			out = append(out, n.Project)
		}
	}
	sort.Strings(out)
	return out
}

func filterProject(notes []store.Note, proj string) []store.Note {
	out := make([]store.Note, 0, len(notes))
	for _, n := range notes {
		if n.Project == proj {
			out = append(out, n)
		}
	}
	return out
}

// packBatches cắt danh sách note thành các batch ≤ limit rune (theo dòng);
// một dòng dài hơn limit đứng riêng một batch.
func packBatches(notes []store.Note, limit int) [][]store.Note {
	var out [][]store.Note
	var cur []store.Note
	size := 0
	for _, n := range notes {
		ln := utf8.RuneCountInString(noteLine(n)) + 1
		if len(cur) > 0 && size+ln > limit {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, n)
		size += ln
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// orNone: project rỗng → "-" (Brain không gắn project của tiến trình khi ghi).
func orNone(p string) string {
	if p == "" {
		return "-"
	}
	return p
}
```

`internal/consolidate/prompts/consolidate.md` (mới):

```markdown
# Consolidate memories: merge duplicate knowledge notes

You receive a numbered list of the user's knowledge notes (decision, fact, preference, procedure) — one per line, formatted `#id [kind] (YYYY-MM-DD) tags: text`. The same information is often stored in several notes: repeated tellings from different sessions, small wording changes, or a newer note that already covers an older one. Find groups that should be ONE note and merge each group.

## Rules

- Merge only notes of the SAME kind that state the SAME thing. Same topic is not enough — merge only when keeping both would be a true duplicate.
- Different versions of the same fact ("port is 8080" vs "port is 9090"): if the dates show one is newer, keep the newer value; if you cannot tell which is newer, do NOT merge.
- A `procedure` merges only with a procedure describing the same method (same goal, same steps). Differing steps → keep both.
- The merged `text` is one self-contained sentence in the user's language, like the originals. Keep names, numbers, dates, and every detail that differs — never drop information a note carried. Fold any reason/context into the text (there is no separate why field).
- Merge at least 2 notes. A note that duplicates nothing must not appear in any merge. Use only ids from the list; never invent notes or ids. Do not output tags — they are combined automatically.
- Merging deletes the old notes permanently. When in doubt, leave it out — precision first.

## Output

Return plain JSON only, exactly this schema, no surrounding text, no markdown fence:

{"merges":[{"kind":"fact","text":"...","notes":[12,31]}]}

No merges worth making → {"merges":[]}.
```

`internal/worker/register.go`:
- Comment dòng 27: `// RegisterAll: embed_chunk; extract_session/summarize_session; transcribe_media; consolidate.`
- Import thêm `"mind-runner/internal/consolidate"`.
- Thêm sau khối transcribe_media:

```go
	cg := consolidate.New(d.Store, d.Brain, d.Egress, d.Config)
	reg.Register("consolidate", func(ctx context.Context, _ json.RawMessage) error {
		return cg.Run(ctx)
	})
```

- [ ] **Step 4: Chạy — pass**

Run: `PATH=/usr/local/go/bin:$PATH go test ./internal/consolidate/ ./internal/cli/ ./internal/store/ -v`
Expected: PASS (gồm cả cycle test và 3 test consolidate).

- [ ] **Step 5: Full suite**

Run: `PATH=/usr/local/go/bin:$PATH go test ./...`
Expected: PASS.

---

### Task 6: Verification cuối

- [ ] **Step 1: Vet + gofmt + build**

Run:

```bash
PATH=/usr/local/go/bin:$PATH gofmt -l internal/ cmd/
PATH=/usr/local/go/bin:$PATH go vet ./...
PATH=/usr/local/go/bin:$PATH go build -o dist/mind-runner ./cmd/mind-runner
```

Expected: `gofmt -l` in ra rỗng (hoặc chỉ file pre-existing — không file nào thuộc plan); vet sạch; build thành công.

- [ ] **Step 2: Full test + đếm supersede còn sót**

Run:

```bash
PATH=/usr/local/go/bin:$PATH go test ./...
grep -rn "SupersedeNote\|IncludeSuperseded\|plan\.supersede" internal/ --include='*.go'
```

Expected: test PASS; grep rỗng.

- [ ] **Step 3: Kiểm tra bằng mắt các điểm nối**

Đọc lại: `internal/cli/maintenance.go` (bước 3b in `consolidate:`), `internal/worker/register.go` (consolidate registered), `internal/consolidate/consolidate.go` (guard + validate), grep `superseded` trong `internal/` — chỉ còn notes.go/purge.go/migrations.

- [ ] **Step 4 (chạy tay, chỉ khi user bảo): maintenance trên DB thật**

`PATH=/usr/local/go/bin:$PATH go run ./cmd/mind-runner maintenance` — cần key thật trong env; kiểm tra trước `backups/` có bản hôm nay. Xác nhận dòng `purge: … superseded=N`, `consolidate: enqueued`. **Không chạy bước này nếu user chưa bảo.**

---

## Ghi chú thực thi

- Thứ tự task tuyến tính: T1 độc lập; T2 phải trước T3 (HardDeleteNotes) và T5; T4 độc lập (chỉ in `superseded=` phụ thuộc T2); T5 phụ thuộc T2+T4.
- Sau mỗi task: `go test ./...` xanh mới sang task kế. Không commit.
- Nếu `StaleRunning`/guard/ngưỡng cần chỉnh sau thực chạy: đổi hằng số trong `consolidate.go` (đều có comment `ponytail:`).

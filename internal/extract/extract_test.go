package extract_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/extract"
	"mind-runner/internal/store"
	"mind-runner/internal/worker"
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

// ccUser: một dòng transcript JSONL của Claude Code (lượt người dùng).
func ccUser(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	return string(b) + "\n"
}

func gzipBlob(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// seedSessionRaw: session space 1 (personal) + các delta gzip seq 1..n; trả id raw đầu tiên.
func seedSessionRaw(t *testing.T, st *store.Store, sessionID string, blobs ...[]byte) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if err := st.UpsertSessionStart(ctx, sessionID, "claude-code", 1, nil, now); err != nil {
		t.Fatal(err)
	}
	var first int64
	for i, b := range blobs {
		id, err := st.InsertRaw(ctx, sessionID, i+1, b, 30, now)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
	}
	return first
}

const chatExtraction = `{
  "notes": [
    {"kind": "fact", "text": "Mind-runner dùng SQLite vì local-first", "tags": ["mind-runner"]},
    {"kind": "note", "text": "Đã bàn xong thiết kế chunker", "tags": []}
  ],
  "tasks": [{"title": "Viết tài liệu kiến trúc", "next_step": "bắt đầu từ sơ đồ luồng"}],
  "relations": [
    {"from": "Viết tài liệu kiến trúc", "to": "Mind-runner dùng SQLite vì local-first", "type": "mentions", "note_index": 0},
    {"from": "chunker", "to": "SQLite", "type": "related"}
  ]
}`

func TestRunSessionWritesNotesTasksRelations(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	rawID := seedSessionRaw(t, st, "s1", gzipBlob(t, ccUser("bàn thiết kế")))

	fake := egressfake.New(t, egressfake.Options{ChatResp: func(string, string) string { return chatExtraction }})
	cfg := egressfake.Config(fake, nil)
	b := brain.New(st, fake.Egress, &cfg)
	x := extract.New(st, b, fake.Egress, &cfg)
	if err := x.RunRaw(ctx, rawID); err != nil {
		t.Fatal(err)
	}

	// notes: đúng kind, source hook:stop, session_id s1
	noteRows, err := st.DB().QueryContext(ctx,
		`SELECT id, kind, source, session_id FROM notes ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer noteRows.Close()
	type noteRow struct {
		id      int64
		kind    string
		source  string
		session string
	}
	var notes []noteRow
	for noteRows.Next() {
		var n noteRow
		if err := noteRows.Scan(&n.id, &n.kind, &n.source, &n.session); err != nil {
			t.Fatal(err)
		}
		notes = append(notes, n)
	}
	if len(notes) != 2 {
		t.Fatalf("notes=%+v", notes)
	}
	if notes[0].kind != "fact" || notes[0].source != "hook:stop" || notes[0].session != "s1" ||
		notes[1].kind != "note" || notes[1].source != "hook:stop" || notes[1].session != "s1" {
		t.Fatalf("notes=%+v", notes)
	}

	// chunks có, embed_chunk queued cho từng note mới
	var nChunks, nEmbed int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE note_id=?`, notes[0].id).Scan(&nChunks); err != nil {
		t.Fatal(err)
	}
	if nChunks == 0 {
		t.Fatal("note mới phải có chunk")
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE type='embed_chunk' AND state='queued'`).Scan(&nEmbed); err != nil {
		t.Fatal(err)
	}
	if nEmbed != 2 {
		t.Fatalf("embed jobs=%d, muốn 2", nEmbed)
	}

	// task + next_step
	var title, status string
	var ns sql.NullString
	if err := st.DB().QueryRowContext(ctx,
		`SELECT title, status, next_step FROM tasks`).Scan(&title, &status, &ns); err != nil {
		t.Fatal(err)
	}
	if title != "Viết tài liệu kiến trúc" || status != "open" || !ns.Valid || ns.String != "bắt đầu từ sơ đồ luồng" {
		t.Fatalf("task: %q %q %+v", title, status, ns)
	}

	// relations: note_index 0 → source_note_id = note đầu; không index → NULL
	relRows, err := st.DB().QueryContext(ctx, `SELECT from_ref, source_note_id FROM relations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer relRows.Close()
	var srcs []sql.NullInt64
	var froms []string
	for relRows.Next() {
		var from string
		var src sql.NullInt64
		if err := relRows.Scan(&from, &src); err != nil {
			t.Fatal(err)
		}
		froms = append(froms, from)
		srcs = append(srcs, src)
	}
	if len(srcs) != 2 {
		t.Fatalf("relations=%v", froms)
	}
	if froms[0] != "Viết tài liệu kiến trúc" || !srcs[0].Valid || srcs[0].Int64 != notes[0].id {
		t.Fatalf("rel0: %q %+v", froms[0], srcs[0])
	}
	if srcs[1].Valid {
		t.Fatalf("rel1 source_note_id=%+v, muốn NULL", srcs[1])
	}

	// extract không sinh episode (một nguồn episode duy nhất: summarize_session)
	var nEpisodes int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM episodes`).Scan(&nEpisodes); err != nil {
		t.Fatal(err)
	}
	if nEpisodes != 0 {
		t.Fatalf("episodes=%d, muốn 0", nEpisodes)
	}

	// chạy lần 2 (retry): notes idempotent, task chống trùng
	if err := x.RunRaw(ctx, rawID); err != nil {
		t.Fatal(err)
	}
	var nNotes, nTasks int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM notes`).Scan(&nNotes); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&nTasks); err != nil {
		t.Fatal(err)
	}
	if nNotes != 2 || nTasks != 1 {
		t.Fatalf("sau retry: notes=%d tasks=%d, muốn 2/1", nNotes, nTasks)
	}
}

func TestSummarizeSessionGunzipsInSeqOrder(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedSessionRaw(t, st, "s1", gzipBlob(t, "nội dung một\n"), gzipBlob(t, "nội dung hai\n"))

	var gotUser string
	fake := egressfake.New(t, egressfake.Options{ChatResp: func(_, user string) string {
		gotUser = user
		return "Phiên bàn thiết kế mind-runner, chốt D12/D13."
	}})
	cfg := egressfake.Config(fake, nil)
	x := extract.New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)

	if err := x.SummarizeSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotUser, "nội dung một") || !strings.Contains(gotUser, "nội dung hai") {
		t.Fatalf("user=%q, thiếu delta", gotUser)
	}
	if strings.Index(gotUser, "nội dung một") > strings.Index(gotUser, "nội dung hai") {
		t.Fatalf("user=%q, sai thứ tự seq", gotUser)
	}

	// chạy lần 2 → seq tăng dần 1, 2
	if err := x.SummarizeSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT seq, summary FROM episodes WHERE session_id='s1' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seqs []int
	for rows.Next() {
		var seq int
		var summary string
		if err := rows.Scan(&seq, &summary); err != nil {
			t.Fatal(err)
		}
		if summary != "Phiên bàn thiết kế mind-runner, chốt D12/D13." {
			t.Fatalf("summary=%q", summary)
		}
		seqs = append(seqs, seq)
	}
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("seqs=%v, muốn [1 2]", seqs)
	}
}

func TestExtractSessionViaWorkerLoop(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	rawID := seedSessionRaw(t, st, "s1", gzipBlob(t, ccUser("chào")))

	fake := egressfake.New(t, egressfake.Options{ChatResp: func(string, string) string { return chatExtraction }})
	cfg := egressfake.Config(fake, nil)
	b := brain.New(st, fake.Egress, &cfg)
	reg := worker.NewRegistry()
	worker.RegisterAll(reg, worker.Deps{Store: st, Brain: b, Egress: fake.Egress, Config: &cfg})

	if _, err := st.Enqueue(ctx, "extract_session", map[string]int64{"raw_id": rawID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// max=1: chỉ claim job extract_session (id nhỏ nhất); embed_chunk để lại queued
	n, err := worker.Run(ctx, st, reg, 1, time.Now)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var state string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT state FROM jobs WHERE type='extract_session'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "done" {
		t.Fatalf("state=%q", state)
	}
	var nNotes int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM notes`).Scan(&nNotes); err != nil {
		t.Fatal(err)
	}
	if nNotes != 2 {
		t.Fatalf("notes=%d", nNotes)
	}
}

// TestRunSessionLocalPolicyNeverTouchesCloud (spec verify): session space
// `work` policy local → Chat đi local, cloud nhận 0 request.
func TestRunSessionLocalPolicyNeverTouchesCloud(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	spaceID, err := st.SpaceByName(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.UpsertSessionStart(ctx, "s-local", "claude-code", spaceID, nil, now); err != nil {
		t.Fatal(err)
	}
	rawID, err := st.InsertRaw(ctx, "s-local", 1, gzipBlob(t, ccUser("chào")), 30, now)
	if err != nil {
		t.Fatal(err)
	}

	cloud := egressfake.New(t, egressfake.Options{})
	local := egressfake.NewLocal(t, egressfake.Options{
		ChatResp: func(string, string) string {
			return `{"notes":[{"kind":"note","text":"công việc local","tags":[]}],"tasks":[],"relations":[]}`
		},
	})
	cfg := egressfake.Config(cloud, local)
	cfg.Spaces.Policy = map[string]string{"work": "local"}
	eg := egress.New(cfg, nil)
	b := brain.New(st, eg, &cfg)
	x := extract.New(st, b, eg, &cfg)

	if err := x.RunRaw(ctx, rawID); err != nil {
		t.Fatal(err)
	}
	if cloud.ChatCalls != 0 {
		t.Fatalf("cloud ChatCalls=%d, muốn 0 (policy local tuyệt đối không rơi cloud)", cloud.ChatCalls)
	}
	if local.ChatCalls != 1 {
		t.Fatalf("local ChatCalls=%d, muốn 1", local.ChatCalls)
	}
}

// TestSummarizeSessionCapsInput: phiên rất dài → chỉ gửi phần cuối (≤ trần) kèm ghi chú cắt.
func TestSummarizeSessionCapsInput(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedSessionRaw(t, st, "s1", gzipBlob(t, strings.Repeat(strings.Repeat("đầu ", 200)+"\n", 100)), gzipBlob(t, strings.Repeat(strings.Repeat("x", 1000)+"\n", 40)+"KẾT\n"))

	var gotUser string
	fake := egressfake.New(t, egressfake.Options{ChatResp: func(_, user string) string {
		gotUser = user
		return "tóm tắt"
	}})
	cfg := egressfake.Config(fake, nil)
	x := extract.New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)
	if err := x.SummarizeSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(gotUser); n > 48_100 {
		t.Fatalf("input %d rune, vượt trần", n)
	}
	if !strings.HasSuffix(strings.TrimSpace(gotUser), "KẾT") || !strings.Contains(gotUser, "Đã cắt bớt") {
		t.Fatalf("thiếu phần cuối hoặc ghi chú cắt: %q…", gotUser[:80])
	}
}

// TestRunSessionStructuredFields: why/alternatives vào notes.meta (và chunk để
// embed), task nhận due/owner/waiting_on; due sai định dạng bị bỏ, task vẫn ghi.
func TestRunSessionStructuredFields(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	rawID := seedSessionRaw(t, st, "s2", gzipBlob(t, ccUser("chốt sqlite")))
	if err := st.SetSessionCWD(ctx, "s2", "/w/mind-runner", "mind-runner"); err != nil {
		t.Fatal(err)
	}
	resp := `{"notes":[{"kind":"decision","text":"Chọn SQLite cho mind-runner","why":"chạy local, không cần server","alternatives":["Postgres"]}],
	"tasks":[{"title":"Gửi báo giá","owner":"Lan","waiting_on":"anh Minh","due":"2026-10-10"},{"title":"Dọn backlog","due":"thứ sáu"}],"relations":[]}`
	fake := egressfake.New(t, egressfake.Options{ChatResp: func(string, string) string { return resp }})
	cfg := egressfake.Config(fake, nil)
	b := brain.New(st, fake.Egress, &cfg)
	x := extract.New(st, b, fake.Egress, &cfg)
	if err := x.RunRaw(ctx, rawID); err != nil {
		t.Fatal(err)
	}
	notes, err := st.NotesByKind(ctx, 1, "decision", time.Time{}, 10)
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes=%+v err=%v", notes, err)
	}
	if notes[0].Project != "mind-runner" {
		t.Fatalf("note phải mang project của phiên: %q", notes[0].Project)
	}
	m := notes[0].Meta
	if m.Why != "chạy local, không cần server" || len(m.Alternatives) != 1 || m.Alternatives[0] != "Postgres" {
		t.Fatalf("meta=%+v", m)
	}
	var chunk string
	if err := st.DB().QueryRowContext(ctx, `SELECT text FROM chunks WHERE note_id=?`, notes[0].ID).Scan(&chunk); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chunk, "Why: chạy local") {
		t.Fatalf("chunk thiếu why: %q", chunk)
	}
	tasks, err := st.OpenTasks(ctx, 1, 10)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	byTitle := map[string]store.Task{}
	for _, tk := range tasks {
		byTitle[tk.Title] = tk
	}
	q := byTitle["Gửi báo giá"]
	if q.Owner != "Lan" || q.WaitingOn != "anh Minh" || q.DueAt != "2026-10-10" || q.Project != "mind-runner" {
		t.Fatalf("task=%+v", q)
	}
	if d := byTitle["Dọn backlog"]; d.DueAt != "" {
		t.Fatalf("due sai định dạng phải bị bỏ: %+v", d)
	}
}

// TestRunSessionWindowsLongDelta: delta dài → nhiều lần gọi model, mỗi lần ≤
// cửa sổ; note của mọi cửa sổ đều được ghi, note_index trỏ đúng note của cửa sổ.
func TestRunSessionWindowsLongDelta(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	var in strings.Builder
	for i := 0; i < 30; i++ {
		in.WriteString(ccUser(fmt.Sprintf("lượt %d ", i) + strings.Repeat("nội dung ", 400)))
	}
	rawID := seedSessionRaw(t, st, "sw", gzipBlob(t, in.String()))

	calls := 0
	fake := egressfake.New(t, egressfake.Options{ChatResp: func(_, user string) string {
		calls++
		if n := utf8.RuneCountInString(user); n > 40_000 {
			t.Errorf("cửa sổ %d rune", n)
		}
		return fmt.Sprintf(`{"notes":[{"kind":"fact","text":"sự kiện cửa sổ %d"}],"relations":[{"from":"a%d","to":"b","type":"related","note_index":0}]}`, calls, calls)
	}})
	cfg := egressfake.Config(fake, nil)
	x := extract.New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)
	if err := x.RunRaw(ctx, rawID); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("calls=%d, muốn ≥2 cửa sổ", calls)
	}
	var notes int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM notes`).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != calls {
		t.Fatalf("notes=%d calls=%d", notes, calls)
	}
	// relation của cửa sổ 2 gắn note thứ 2 (không phải note 0)
	var text string
	err := st.DB().QueryRowContext(ctx, `SELECT n.text FROM relations r JOIN notes n ON n.id=r.source_note_id WHERE r.from_ref='a2'`).Scan(&text)
	if err != nil || text != "sự kiện cửa sổ 2" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

// TestRunSessionBatchesPendingRaws: nhiều lượt Stop của một phiên → một job
// duy nhất, một lần gọi model chứa đủ các lượt theo thứ tự; chạy lại không
// gửi lại; lượt mới sau đó → job mới.
func TestRunSessionBatchesPendingRaws(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	if err := st.UpsertSessionStart(ctx, "sb", "claude-code", 1, nil, now); err != nil {
		t.Fatal(err)
	}
	off := int64(0)
	appendTurn := func(text string) {
		t.Helper()
		if _, err := st.AppendRaw(ctx, "sb", gzipBlob(t, ccUser(text)), 30, now, now.Add(10*time.Minute), off, off+1); err != nil {
			t.Fatal(err)
		}
		off++
	}
	appendTurn("lượt một")
	appendTurn("lượt hai")
	appendTurn("lượt ba")
	var jobs int
	var payload, runAfter string
	st.DB().QueryRowContext(ctx, `SELECT count(*), max(payload), max(run_after) FROM jobs WHERE type='extract_session'`).Scan(&jobs, &payload, &runAfter)
	if jobs != 1 || payload != `{"session_id":"sb"}` || !strings.HasPrefix(runAfter, "2026-10-07T09:10:00") {
		t.Fatalf("jobs=%d payload=%s run_after=%s", jobs, payload, runAfter)
	}

	// phiên kết thúc → chạy ngay
	if err := st.ExpediteExtract(ctx, "sb", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	st.DB().QueryRowContext(ctx, `SELECT run_after FROM jobs WHERE type='extract_session'`).Scan(&runAfter)
	if !strings.HasPrefix(runAfter, "2026-10-07T09:01:00") {
		t.Fatalf("run_after=%s", runAfter)
	}

	var inputs []string
	fake := egressfake.New(t, egressfake.Options{ChatResp: func(_, user string) string {
		inputs = append(inputs, user)
		return `{"notes":[{"kind":"fact","text":"gộp ok"}]}`
	}})
	cfg := egressfake.Config(fake, nil)
	x := extract.New(st, brain.New(st, fake.Egress, &cfg), fake.Egress, &cfg)
	if err := x.RunSession(ctx, "sb"); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0] != "User: lượt một\nUser: lượt hai\nUser: lượt ba\n" {
		t.Fatalf("inputs=%q", inputs)
	}
	if err := x.RunSession(ctx, "sb"); err != nil || len(inputs) != 1 {
		t.Fatalf("chạy lại vẫn gửi: %d err=%v", len(inputs), err)
	}
	// lượt mới: job cũ (queued, chưa claim trong test) vẫn gom; chạy → chỉ lượt mới
	appendTurn("lượt bốn")
	if err := x.RunSession(ctx, "sb"); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 || inputs[1] != "User: lượt bốn\n" {
		t.Fatalf("inputs=%q", inputs)
	}
}

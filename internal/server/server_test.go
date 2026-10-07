package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mind-runner/internal/brain"
	"mind-runner/internal/config"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/execx"
	"mind-runner/internal/media"
	"mind-runner/internal/store"
)

// testCfg: config tối thiểu — mọi space theo policy mặc định "cloud".
func testCfg() *config.Config {
	cfg := config.Default()
	return &cfg
}

// testMedia: Media service cho test tool (không chạy transcribe trong test này).
func testMedia(t *testing.T, st *store.Store) *media.Media {
	t.Helper()
	cfg := testCfg()
	return media.New(st, brain.New(st, nil, cfg), nil, cfg, execx.OS{}, t.TempDir())
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

func TestRememberTool(t *testing.T) {
	st := newStore(t)
	srv := New(brain.New(st, nil, testCfg()), st, testMedia(t, st))

	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	// tools/list có remember
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range tools.Tools {
		if tl.Name == "remember" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tools=%v", tools.Tools)
	}

	// gọi remember lần 1 → note mới
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remember",
		Arguments: map[string]any{"text": "xin chào", "tags": []string{"test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("isError: %v", res.Content)
	}
	var out RememberOut
	if err := remarshal(res.StructuredContent, &out); err != nil {
		t.Fatalf("structured output: %v", err)
	}
	if !out.Created || out.NoteID <= 0 {
		t.Fatalf("out=%+v", out)
	}

	var n int
	var kind string
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(kind),'') FROM notes`).Scan(&n, &kind); err != nil {
		t.Fatal(err)
	}
	if n != 1 || kind != "note" {
		t.Fatalf("notes=%d kind=%s", n, kind)
	}

	// gọi lặp → created=false, cùng id
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remember",
		Arguments: map[string]any{"text": "xin chào", "tags": []string{"test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out2 RememberOut
	if err := remarshal(res.StructuredContent, &out2); err != nil {
		t.Fatal(err)
	}
	if out2.Created || out2.NoteID != out.NoteID {
		t.Fatalf("out2=%+v, muốn created=false id=%d", out2, out.NoteID)
	}

	// text rỗng → isError
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remember",
		Arguments: map[string]any{"text": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("text rỗng phải isError: %v", res)
	}
}

func remarshal(v any, dst any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// TestRecallTool: recall qua in-memory transport — ghi bằng remember rồi tìm
// lại; RecallOut parse được; space sai → isError kèm danh sách space hợp lệ.
func TestRecallTool(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{})
	srv := New(brain.New(st, fake.Egress, testCfg()), st, testMedia(t, st))

	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	// tools/list có recall
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range tools.Tools {
		if tl.Name == "recall" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tools=%v", tools.Tools)
	}

	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remember",
		Arguments: map[string]any{"text": "ghi chú alpha"},
	}); err != nil || res.IsError {
		t.Fatalf("seed remember: res=%v err=%v", res, err)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "recall",
		Arguments: map[string]any{"query": "alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("isError: %v", res.Content)
	}
	var out RecallOut
	if err := remarshal(res.StructuredContent, &out); err != nil {
		t.Fatalf("structured output: %v", err)
	}
	if len(out.Hits) != 1 || out.Hits[0].Space != "personal" || out.Hits[0].Text != "ghi chú alpha" {
		t.Fatalf("out=%+v", out)
	}
	if out.Stages["fts"] != "ok:1" {
		t.Fatalf("stages=%v", out.Stages)
	}

	// space sai → isError liệt kê space hợp lệ
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "recall",
		Arguments: map[string]any{"query": "alpha", "spaces": []string{"không-có"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("space sai phải isError: %v", res)
	}
	var msg strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			msg.WriteString(tc.Text)
		}
	}
	if !strings.Contains(msg.String(), "personal") {
		t.Fatalf("lỗi phải liệt kê space hợp lệ: %v", res.Content)
	}
}

// TestUsageCounters: remember/recall cộng stats.* vào meta; IncrMeta lỗi
// (bảng meta mất) → tool vẫn thành công — counter không làm hỏng đường ghi.
func TestUsageCounters(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{})
	srv := New(brain.New(st, fake.Egress, testCfg()), st, testMedia(t, st))

	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember", Arguments: map[string]any{"text": "đếm dùng"},
	}); err != nil || res.IsError {
		t.Fatalf("res=%v err=%v", res, err)
	}
	for i := 0; i < 2; i++ {
		if res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "recall", Arguments: map[string]any{"query": "đếm"},
		}); err != nil || res.IsError {
			t.Fatalf("recall %d: res=%v err=%v", i, res, err)
		}
	}
	for key, want := range map[string]string{"stats.remember_calls": "1", "stats.recall_calls": "2"} {
		v, ok, err := st.GetMeta(ctx, key)
		if err != nil || !ok || v != want {
			t.Fatalf("%s=%q ok=%v err=%v, muốn %q", key, v, ok, err, want)
		}
	}

	// IncrMeta lỗi → tool vẫn thành công
	if _, err := st.DB().ExecContext(ctx, `ALTER TABLE meta RENAME TO meta_bak`); err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember", Arguments: map[string]any{"text": "vẫn ghi được"},
	})
	if err != nil || res.IsError {
		t.Fatalf("counter lỗi không được fail tool: res=%v err=%v", res, err)
	}
}

// TestTaskTools: task_add → task_list thấy (stale=false, next_step giữ);
// task_update done → list open rỗng; task cũ 20 ngày → stale=true; status lạ
// → isError; task_list readOnly hint; id sai / title rỗng → isError.
func TestTaskTools(t *testing.T) {
	st := newStore(t)
	srv := New(brain.New(st, nil, testCfg()), st, testMedia(t, st))

	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = tl
	}
	for _, want := range []string{"task_add", "task_update", "task_list"} {
		if names[want] == nil {
			t.Fatalf("thiếu tool %s: %v", want, tools.Tools)
		}
	}
	if a := names["task_list"].Annotations; a == nil || !a.ReadOnlyHint {
		t.Fatalf("task_list phải readOnly: %+v", a)
	}

	// add → list thấy
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "task_add",
		Arguments: map[string]any{"title": "Rà soát M5", "next_step": "đọc lại plan"},
	})
	if err != nil || res.IsError {
		t.Fatalf("task_add: res=%v err=%v", res, err)
	}
	var addOut TaskAddOut
	if err := remarshal(res.StructuredContent, &addOut); err != nil || addOut.TaskID <= 0 {
		t.Fatalf("addOut=%+v err=%v", addOut, err)
	}

	listOpen := func() TaskListOut {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "task_list", Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Fatalf("task_list: res=%v err=%v", res, err)
		}
		var out TaskListOut
		if err := remarshal(res.StructuredContent, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	out := listOpen()
	if len(out.Tasks) != 1 {
		t.Fatalf("tasks=%+v", out.Tasks)
	}
	it := out.Tasks[0]
	if it.ID != addOut.TaskID || it.Title != "Rà soát M5" || it.Status != "open" || it.Stale || it.NextStep != "đọc lại plan" || it.UpdatedAt == "" {
		t.Fatalf("item=%+v", it)
	}

	// update done → list open rỗng; list status done thấy lại
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "task_update",
		Arguments: map[string]any{"task_id": addOut.TaskID, "status": "done"},
	})
	if err != nil || res.IsError {
		t.Fatalf("task_update: res=%v err=%v", res, err)
	}
	var upOut TaskUpdateOut
	if err := remarshal(res.StructuredContent, &upOut); err != nil || !upOut.Updated || upOut.Status != "done" {
		t.Fatalf("upOut=%+v err=%v", upOut, err)
	}
	if got := listOpen(); len(got.Tasks) != 0 {
		t.Fatalf("sau done, open=%+v", got.Tasks)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "task_list",
		Arguments: map[string]any{"status": "done"},
	})
	if err != nil || res.IsError {
		t.Fatalf("task_list done: res=%v err=%v", res, err)
	}
	var gotDone TaskListOut
	if err := remarshal(res.StructuredContent, &gotDone); err != nil || len(gotDone.Tasks) != 1 || gotDone.Tasks[0].ID != addOut.TaskID {
		t.Fatalf("done=%+v err=%v", gotDone, err)
	}

	// task cũ 20 ngày → stale=true
	old := time.Now().UTC().AddDate(0, 0, -20).Format(time.RFC3339Nano)
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO tasks(space_id, title, status, updated_at) VALUES(1, 'việc cũ', 'open', ?)`, old); err != nil {
		t.Fatal(err)
	}
	out = listOpen()
	if len(out.Tasks) != 1 || out.Tasks[0].Title != "việc cũ" || !out.Tasks[0].Stale {
		t.Fatalf("stale=%+v", out.Tasks)
	}

	// status lạ → isError
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "task_update",
		Arguments: map[string]any{"task_id": addOut.TaskID, "status": "xxx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("status lạ phải isError: %v", res)
	}

	// id sai → isError; title rỗng → isError
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "task_update",
		Arguments: map[string]any{"task_id": 9999, "status": "done"},
	})
	if err != nil || !res.IsError {
		t.Fatalf("id sai: res=%v err=%v", res, err)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "task_add",
		Arguments: map[string]any{"title": "  "},
	})
	if err != nil || !res.IsError {
		t.Fatalf("title rỗng: res=%v err=%v", res, err)
	}
}

// TestIngestTool: tool ingest — created=true lần đầu, lần 2 created=false cùng
// id; ResolveSession desktop mở session trước khi ghi; file thiếu / kind
// caption / space sai → isError; annotation idempotent có mặt.
func TestIngestTool(t *testing.T) {
	st := newStore(t)
	srv := New(brain.New(st, nil, testCfg()), st, testMedia(t, st))

	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range tools.Tools {
		if tl.Name == "ingest" {
			found = true
			if a := tl.Annotations; a == nil || !a.IdempotentHint {
				t.Fatalf("ingest phải idempotent: %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("tools=%v", tools.Tools)
	}

	path := filepath.Join(t.TempDir(), "tài liệu.md")
	if err := os.WriteFile(path, []byte("nội dung tài liệu"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ingest",
		Arguments: map[string]any{"path": path},
	})
	if err != nil || res.IsError {
		t.Fatalf("ingest: res=%v err=%v", res, err)
	}
	var out IngestOut
	if err := remarshal(res.StructuredContent, &out); err != nil || out.NoteID <= 0 || !out.Created {
		t.Fatalf("out=%+v err=%v", out, err)
	}

	// ingest là event mở/touch session desktop
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE client='claude-desktop'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("desktop sessions=%d, muốn 1", n)
	}

	// lặp → created=false, cùng id
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ingest",
		Arguments: map[string]any{"path": path},
	})
	if err != nil || res.IsError {
		t.Fatalf("ingest lần 2: res=%v err=%v", res, err)
	}
	var out2 IngestOut
	if err := remarshal(res.StructuredContent, &out2); err != nil || out2.Created || out2.NoteID != out.NoteID {
		t.Fatalf("out2=%+v err=%v", out2, err)
	}

	// file thiếu → isError
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ingest",
		Arguments: map[string]any{"path": filepath.Join(t.TempDir(), "khong-co.md")},
	})
	if err != nil || !res.IsError {
		t.Fatalf("file thiếu: res=%v err=%v", res, err)
	}

	// kind caption → isError
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ingest",
		Arguments: map[string]any{"path": path, "kind": "caption"},
	})
	if err != nil || !res.IsError {
		t.Fatalf("kind caption: res=%v err=%v", res, err)
	}

	// space sai → isError liệt kê hợp lệ
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ingest",
		Arguments: map[string]any{"path": path, "space": "không-có"},
	})
	if err != nil || !res.IsError {
		t.Fatalf("space sai: res=%v err=%v", res, err)
	}
	var msg strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			msg.WriteString(tc.Text)
		}
	}
	if !strings.Contains(msg.String(), "personal") {
		t.Fatalf("lỗi phải liệt kê space hợp lệ: %v", res.Content)
	}
}

// TestForgetTool: forget note → recall/briefing không thấy; forget task →
// task_list không thấy; lần 2 → forgotten=false có reason; id sai → structured
// forgotten=false (không isError); kind lạ → isError; remember lại → hồi sinh;
// annotation destructive.
func TestForgetTool(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{})
	srv := New(brain.New(st, fake.Egress, testCfg()), st, testMedia(t, st))

	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range tools.Tools {
		if tl.Name == "forget" {
			found = true
			if a := tl.Annotations; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Fatalf("forget phải destructive: %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("tools=%v", tools.Tools)
	}

	// seed note + task
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remember",
		Arguments: map[string]any{"text": "secretcodename ghi nhớ tạm"},
	})
	if err != nil || res.IsError {
		t.Fatalf("remember: res=%v err=%v", res, err)
	}
	var rem RememberOut
	if err := remarshal(res.StructuredContent, &rem); err != nil {
		t.Fatal(err)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "task_add",
		Arguments: map[string]any{"title": "việc sắp bỏ"},
	})
	if err != nil || res.IsError {
		t.Fatalf("task_add: res=%v err=%v", res, err)
	}
	var ta TaskAddOut
	if err := remarshal(res.StructuredContent, &ta); err != nil {
		t.Fatal(err)
	}

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}

	// trước khi quên: recall + briefing thấy note
	var rec RecallOut
	if err := remarshal(call("recall", map[string]any{"query": "secretcodename"}).StructuredContent, &rec); err != nil || len(rec.Hits) != 1 {
		t.Fatalf("recall trước: %+v err=%v", rec, err)
	}
	var bf BriefingOut
	if err := remarshal(call("briefing", map[string]any{"force": true}).StructuredContent, &bf); err != nil || !strings.Contains(bf.Text, "secretcodename") {
		t.Fatalf("briefing trước phải có note: %+v err=%v", bf, err)
	}

	// forget note → ẩn khỏi recall/briefing, có hint
	var fo ForgetOut
	if err := remarshal(call("forget", map[string]any{"kind": "note", "id": rem.NoteID}).StructuredContent, &fo); err != nil || !fo.Forgotten || fo.Hint == "" {
		t.Fatalf("forget note: %+v err=%v", fo, err)
	}
	if err := remarshal(call("recall", map[string]any{"query": "secretcodename"}).StructuredContent, &rec); err != nil || len(rec.Hits) != 0 {
		t.Fatalf("recall sau forget: %+v err=%v", rec, err)
	}
	if err := remarshal(call("briefing", map[string]any{"force": true}).StructuredContent, &bf); err != nil || strings.Contains(bf.Text, "secretcodename") {
		t.Fatalf("briefing sau forget còn note: %+v err=%v", bf, err)
	}

	// forget task → task_list không thấy
	if err := remarshal(call("forget", map[string]any{"kind": "task", "id": ta.TaskID}).StructuredContent, &fo); err != nil || !fo.Forgotten {
		t.Fatalf("forget task: %+v err=%v", fo, err)
	}
	var tl TaskListOut
	if err := remarshal(call("task_list", map[string]any{}).StructuredContent, &tl); err != nil || len(tl.Tasks) != 0 {
		t.Fatalf("task_list sau forget: %+v err=%v", tl, err)
	}

	// lần 2 → forgotten=false + reason (structured, không isError)
	res = call("forget", map[string]any{"kind": "note", "id": rem.NoteID})
	if res.IsError {
		t.Fatalf("lần 2 không được isError: %v", res)
	}
	if err := remarshal(res.StructuredContent, &fo); err != nil || fo.Forgotten || fo.Reason == "" {
		t.Fatalf("forget lần 2: %+v err=%v", fo, err)
	}
	// id sai → forgotten=false
	if err := remarshal(call("forget", map[string]any{"kind": "note", "id": 9999}).StructuredContent, &fo); err != nil || fo.Forgotten {
		t.Fatalf("id sai: %+v err=%v", fo, err)
	}
	// kind lạ → isError
	if res := call("forget", map[string]any{"kind": "xxx", "id": 1}); !res.IsError {
		t.Fatalf("kind lạ phải isError: %v", res)
	}

	// hồi sinh: remember lại y hệt → created=false + thấy lại
	res = call("remember", map[string]any{"text": "secretcodename ghi nhớ tạm"})
	var rem2 RememberOut
	if err := remarshal(res.StructuredContent, &rem2); err != nil || rem2.Created || rem2.NoteID != rem.NoteID {
		t.Fatalf("hồi sinh: %+v err=%v", rem2, err)
	}
	if err := remarshal(call("recall", map[string]any{"query": "secretcodename"}).StructuredContent, &rec); err != nil || len(rec.Hits) != 1 {
		t.Fatalf("recall sau hồi sinh: %+v err=%v", rec, err)
	}
}

// TestExportTool: tool export — counts khớp, file note tồn tại; annotation
// idempotent.
func TestExportTool(t *testing.T) {
	st := newStore(t)
	srv := New(brain.New(st, nil, testCfg()), st, testMedia(t, st))

	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range tools.Tools {
		if tl.Name == "export" {
			found = true
			if a := tl.Annotations; a == nil || !a.IdempotentHint {
				t.Fatalf("export phải idempotent: %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("tools=%v", tools.Tools)
	}

	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "remember", Arguments: map[string]any{"text": "ghi chú để export"},
	}); err != nil || res.IsError {
		t.Fatalf("remember: res=%v err=%v", res, err)
	}

	outDir := t.TempDir()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "export",
		Arguments: map[string]any{"dir": outDir},
	})
	if err != nil || res.IsError {
		t.Fatalf("export: res=%v err=%v", res, err)
	}
	var out ExportOut
	if err := remarshal(res.StructuredContent, &out); err != nil || out.Dir != outDir || out.Notes != 1 || out.Tasks != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	matches, err := filepath.Glob(filepath.Join(outDir, "notes", "personal", "note", "*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("matches=%v err=%v", matches, err)
	}
}

// TestBriefingTool: gate 1 lần/ngày + force; ResolveSession desktop chạy trước
// assemble (30' reuse: 2 lần trong 30' cùng session, +31' session mới).
func TestBriefingTool(t *testing.T) {
	st := newStore(t)
	fake := egressfake.New(t, egressfake.Options{})
	srv := New(brain.New(st, fake.Egress, testCfg()), st, testMedia(t, st))
	ctx := context.Background()

	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(10 * time.Minute), base.Add(41 * time.Minute)}
	old := nowFunc
	nowFunc = func() time.Time { v := times[0]; times = times[1:]; return v }
	t.Cleanup(func() { nowFunc = old })

	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO tasks(space_id, title, status, next_step, updated_at) VALUES(1,?,'open',?,?)`,
		"Chuẩn bị demo mind-runner", "viết README", base.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertNote(ctx, &store.Note{
		SpaceID: 1, Kind: "decision", Text: "Dùng SQLite thay Postgres",
		Source: "test", CreatedAt: base.AddDate(0, 0, -1), UpdatedAt: base.AddDate(0, 0, -1),
	}); err != nil {
		t.Fatal(err)
	}

	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	// lần 1: force → delivered, có nội dung
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "briefing",
		Arguments: map[string]any{"force": true},
	})
	if err != nil || res.IsError {
		t.Fatalf("res=%v err=%v", res, err)
	}
	var out BriefingOut
	if err := remarshal(res.StructuredContent, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Delivered || !strings.Contains(out.Text, "Chuẩn bị demo mind-runner") || !strings.Contains(out.Text, "Dùng SQLite thay Postgres") {
		t.Fatalf("lần 1: %+v", out)
	}

	// lần 2: cùng ngày không force → chặn, không assemble
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "briefing", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("res=%v err=%v", res, err)
	}
	var out2 BriefingOut
	if err := remarshal(res.StructuredContent, &out2); err != nil {
		t.Fatal(err)
	}
	if out2.Delivered || out2.Reason != "already_briefed_today" || out2.Text != "" {
		t.Fatalf("lần 2: %+v", out2)
	}

	// lần 3: force → delivered lại
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "briefing",
		Arguments: map[string]any{"force": true},
	})
	if err != nil || res.IsError {
		t.Fatalf("res=%v err=%v", res, err)
	}
	var out3 BriefingOut
	if err := remarshal(res.StructuredContent, &out3); err != nil {
		t.Fatal(err)
	}
	if !out3.Delivered {
		t.Fatalf("lần 3: %+v", out3)
	}

	// desktop sessions: base → tạo mới; +10' (≤30') → touch cùng session;
	// +41' so với lần touch trước (31') → session mới.
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE client='claude-desktop'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("desktop sessions=%d, muốn 2", n)
	}
	var lastSeen string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT MAX(last_seen_at) FROM sessions WHERE client='claude-desktop'`).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	want := store.TS(base.Add(41 * time.Minute))
	if lastSeen != want {
		t.Fatalf("last_seen=%s, muốn %s", lastSeen, want)
	}
}

// TestPrompts (7.4): prompts/list đủ 3; prompts/get trả message hướng dẫn
// đúng tool; argument space xuất hiện trong text.
func TestPrompts(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	srv := New(brain.New(st, nil, testCfg()), st, testMedia(t, st))
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	pl, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range pl.Prompts {
		names[p.Name] = true
	}
	for _, want := range []string{"weekly-review", "meeting-notes", "study-session"} {
		if !names[want] {
			t.Fatalf("thiếu prompt %q, có: %v", want, names)
		}
	}

	// weekly-review: hướng dẫn recall + task_list; space truyền vào xuất hiện trong text
	pr, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name: "weekly-review", Arguments: map[string]string{"space": "work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, m := range pr.Messages {
		tc, ok := m.Content.(*mcp.TextContent)
		if !ok {
			t.Fatalf("content không phải text: %T", m.Content)
		}
		sb.WriteString(tc.Text)
	}
	text := sb.String()
	for _, want := range []string{"task_list", "recall", "work"} {
		if !strings.Contains(text, want) {
			t.Fatalf("weekly-review thiếu %q\ntext=%s", want, text)
		}
	}

	// meeting-notes: remember kind decision + task_add; study-session: fact
	pr, err = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "meeting-notes"})
	if err != nil {
		t.Fatal(err)
	}
	tc := pr.Messages[0].Content.(*mcp.TextContent)
	if !strings.Contains(tc.Text, "decision") || !strings.Contains(tc.Text, "task_add") {
		t.Fatalf("meeting-notes: %s", tc.Text)
	}
	pr, err = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "study-session"})
	if err != nil {
		t.Fatal(err)
	}
	tc = pr.Messages[0].Content.(*mcp.TextContent)
	if !strings.Contains(tc.Text, "fact") || !strings.Contains(tc.Text, "recall") {
		t.Fatalf("study-session: %s", tc.Text)
	}
}

// TestInstructionsSent: initialize trả instructions → client tự nạp, không cần
// người dùng dán custom instructions.
func TestInstructionsSent(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	srv := New(brain.New(st, nil, testCfg()), st, testMedia(t, st))
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	got := cs.InitializeResult().Instructions
	for _, want := range []string{"briefing", "remember", "recall"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions thiếu %q: %q", want, got)
		}
	}
}

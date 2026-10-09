package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mind-runner/internal/brain"
)

func connectTest(t *testing.T) (*mcp.ClientSession, func()) {
	t.Helper()
	st := newStore(t)
	srv := New(brain.New(st, nil, testCfg()), st, testMedia(t, st))
	ctx := context.Background()
	stT, ctT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, stT, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ctT, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs, func() { cs.Close(); ss.Close() }
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && !res.IsError {
		if err := remarshal(res.StructuredContent, out); err != nil {
			t.Fatal(err)
		}
	}
	return res
}

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

// TestStructuredTasks: task_add có hạn/người chờ; task_update "-" xoá trường;
// hạn sai định dạng bị từ chối.
func TestStructuredTasks(t *testing.T) {
	cs, done := connectTest(t)
	defer done()

	var add TaskAddOut
	call(t, cs, "task_add", map[string]any{"title": "Gửi báo giá", "due": "2020-01-01", "waiting_on": "anh Minh", "why": "khách cần"}, &add)
	if !add.Created {
		t.Fatalf("add=%+v", add)
	}
	var list TaskListOut
	call(t, cs, "task_list", map[string]any{}, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].Due != "2020-01-01" || !list.Tasks[0].Overdue ||
		list.Tasks[0].WaitingOn != "anh Minh" || list.Tasks[0].Why != "khách cần" {
		t.Fatalf("list=%+v", list)
	}
	if r := call(t, cs, "task_update", map[string]any{"task_id": add.TaskID, "waiting_on": "-"}, nil); r.IsError {
		t.Fatalf("update: %v", r.Content)
	}
	var after TaskListOut // biến mới: Unmarshal vào struct cũ giữ trường omitempty vắng mặt
	call(t, cs, "task_list", map[string]any{}, &after)
	if after.Tasks[0].WaitingOn != "" || after.Tasks[0].Due != "2020-01-01" {
		t.Fatalf("sau clear: %+v", after.Tasks[0])
	}
	if r := call(t, cs, "task_add", map[string]any{"title": "x", "due": "thứ sáu"}, nil); !r.IsError {
		t.Fatal("due sai định dạng phải isError")
	}
}

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

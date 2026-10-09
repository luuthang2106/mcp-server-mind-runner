package extract_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"mind-runner/internal/brain"
	"mind-runner/internal/egress/egressfake"
	"mind-runner/internal/extract"
	"mind-runner/internal/store"
)

// vecFor: vector giả theo chủ đề — câu cùng chủ đề "db" gần như trùng nhau.
func vecFor(text string) []float32 {
	switch {
	case strings.Contains(text, "SQLite") || strings.Contains(text, "Postgres"):
		return []float32{1, 0, 0, 0}
	case strings.Contains(text, "Neovim"):
		return []float32{0, 1, 0, 0}
	default:
		return []float32{0, 0, 1, 0.2}
	}
}

// TestRunSessionAccuracy: (1) quyết định gần trùng thay thế (xoá cứng) quyết định cũ,
// (2) hai note y hệt trong một lần trích chỉ ghi một, (3) model thấy việc đang
// mở và đóng đúng việc; id bịa bị bỏ, (4) việc trùng tiêu đề (khác dấu câu,
// hoa/thường) không tạo mới.
func TestRunSessionAccuracy(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	var input string
	var chat string
	fake := egressfake.New(t, egressfake.Options{
		EmbedVec: vecFor,
		ChatResp: func(_, user string) string { input = user; return chat },
	})
	cfg := egressfake.Config(fake, nil)
	b := brain.New(st, fake.Egress, &cfg)
	x := extract.New(st, b, fake.Egress, &cfg)

	old, err := b.WriteNote(ctx, brain.WriteParams{SpaceID: 1, Kind: "decision", Text: "Dùng SQLite cho storage", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EmbedNote(ctx, old.NoteID); err != nil {
		t.Fatal(err)
	}
	quote, _, err := st.InsertTask(ctx, 1, "Gửi báo giá cho khách", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := st.InsertTask(ctx, 1, "Viết tài liệu", nil, time.Now())

	seedSessionRaw(t, st, "acc", gzipBlob(t, ccUser("chuyển sang Postgres nhé, báo giá gửi rồi")))
	chat = `{"notes":[
	  {"kind":"decision","text":"Dùng Postgres cho storage","why":"cần truy vấn đồng thời"},
	  {"kind":"preference","text":"Người dùng dùng Neovim"},
	  {"kind":"preference","text":"Người dùng dùng Neovim làm editor"}],
	 "tasks":[{"title":"gửi báo giá cho khách!"}],
	 "task_updates":[{"id":` + itoa(quote) + `,"status":"done"},{"id":99999,"status":"done"},{"id":` + itoa(other) + `,"status":"weird","next_step":"viết phần cài đặt"}]}`
	if err := x.RunSession(ctx, "acc"); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(input, "#"+itoa(quote)+": Gửi báo giá cho khách") || !strings.Contains(input, "Transcript:\nUser: chuyển sang Postgres") {
		t.Fatalf("input thiếu danh sách việc đang mở:\n%s", input)
	}
	// quyết định cũ bị xoá cứng (không còn trong DB)
	if _, err := st.FetchNote(ctx, old.NoteID); err == nil {
		t.Fatal("quyết định cũ phải bị xoá cứng")
	}
	var prefs, tasks int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM notes WHERE kind='preference'`).Scan(&prefs)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM tasks`).Scan(&tasks)
	if prefs != 1 {
		t.Fatalf("preference trùng trong một lần trích: %d", prefs)
	}
	if tasks != 2 {
		t.Fatalf("việc trùng tiêu đề không được tạo mới: %d", tasks)
	}
	q, _ := st.TaskByID(ctx, quote)
	o, _ := st.TaskByID(ctx, other)
	if q.Status != "done" {
		t.Fatalf("việc đã xong phải đóng: %+v", q)
	}
	if o.Status != "open" || o.NextStep == nil || *o.NextStep != "viết phần cài đặt" {
		t.Fatalf("status lạ bị bỏ, next_step vẫn cập nhật: %+v", o)
	}
}

func TestNormTitle(t *testing.T) {
	if store.NormTitle("  Gửi báo giá — cho Khách!! ") != "gui bao gia cho khach" {
		t.Fatal(store.NormTitle("  Gửi báo giá — cho Khách!! "))
	}
	if store.NormTitle("Đặt lịch") != "dat lich" {
		t.Fatal(store.NormTitle("Đặt lịch"))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

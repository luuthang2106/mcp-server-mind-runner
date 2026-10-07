package extract

import (
	"strings"
	"testing"
)

func TestParseExtractionClean(t *testing.T) {
	in := `{
	  "notes": [
	    {"kind": "fact", "text": "Mind-runner dùng SQLite vì local-first", "tags": ["mind-runner"]},
	    {"kind": "note", "text": "Đã chốt xong thiết kế D12/D13"}
	  ],
	  "tasks": [{"title": "Viết tài liệu kiến trúc", "next_step": "bắt đầu từ sơ đồ luồng"}],
	  "relations": [{"from": "Viết tài liệu kiến trúc", "to": "Mind-runner dùng SQLite vì local-first", "type": "mentions", "note_index": 0}]
	}`
	ex, err := ParseExtraction(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Notes) != 2 {
		t.Fatalf("notes=%+v", ex.Notes)
	}
	if ex.Notes[0].Kind != "fact" || ex.Notes[0].Text != "Mind-runner dùng SQLite vì local-first" ||
		len(ex.Notes[0].Tags) != 1 || ex.Notes[0].Tags[0] != "mind-runner" {
		t.Fatalf("notes[0]=%+v", ex.Notes[0])
	}
	if ex.Notes[1].Kind != "note" || ex.Notes[1].Text != "Đã chốt xong thiết kế D12/D13" {
		t.Fatalf("notes[1]=%+v", ex.Notes[1])
	}
	if len(ex.Tasks) != 1 || ex.Tasks[0].Title != "Viết tài liệu kiến trúc" || ex.Tasks[0].NextStep != "bắt đầu từ sơ đồ luồng" {
		t.Fatalf("tasks=%+v", ex.Tasks)
	}
	if len(ex.Relations) != 1 || ex.Relations[0].From != "Viết tài liệu kiến trúc" ||
		ex.Relations[0].Type != "mentions" || ex.Relations[0].NoteIndex == nil || *ex.Relations[0].NoteIndex != 0 {
		t.Fatalf("relations=%+v", ex.Relations)
	}
}

func TestParseExtractionFence(t *testing.T) {
	in := "```json\n{\"notes\":[{\"kind\":\"note\",\"text\":\"đã cài đặt xong\"}],\"tasks\":[],\"relations\":[]}\n```\n"
	ex, err := ParseExtraction(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Notes) != 1 || ex.Notes[0].Kind != "note" || ex.Notes[0].Text != "đã cài đặt xong" {
		t.Fatalf("notes=%+v", ex.Notes)
	}
}

func TestParseExtractionEmptyOK(t *testing.T) {
	ex, err := ParseExtraction(`{"notes":[],"tasks":[],"relations":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Notes) != 0 || len(ex.Tasks) != 0 || len(ex.Relations) != 0 {
		t.Fatalf("ex=%+v", ex)
	}
}

func TestParseExtractionErrors(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantSub []string // chuỗi phải xuất hiện trong error (rỗng = chỉ cần lỗi)
	}{
		{"kind lạ", `{"notes":[{"kind":"idea","text":"x"}],"tasks":[],"relations":[]}`, []string{"notes[0]", "idea"}},
		{"text rỗng", `{"notes":[{"kind":"note","text":"   "}],"tasks":[],"relations":[]}`, []string{"notes[0]"}},
		{"title rỗng", `{"notes":[],"tasks":[{"title":"","next_step":"x"}],"relations":[]}`, []string{"tasks[0]"}},
		{"JSON hỏng", `{"notes": [`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseExtraction(c.in)
			if err == nil {
				t.Fatal("muốn lỗi, nhận nil")
			}
			for _, sub := range c.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Fatalf("err=%q, thiếu %q", err.Error(), sub)
				}
			}
		})
	}
}

// TestParseExtractionProseAroundJSON: lời dẫn trước/sau JSON + kind viết hoa vẫn parse.
func TestParseExtractionProseAroundJSON(t *testing.T) {
	in := "Đây là kết quả:\n```json\n{\"notes\":[{\"kind\":\"Fact\",\"text\":\"x\"}],\"tasks\":[],\"relations\":[]}\n```\nXong."
	ex, err := ParseExtraction(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Notes) != 1 || ex.Notes[0].Kind != "fact" {
		t.Fatalf("ex=%+v", ex)
	}
}
